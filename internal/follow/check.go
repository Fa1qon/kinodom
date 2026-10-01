package follow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/library"
	"kinodom/internal/meta"
	"kinodom/internal/source"
	"kinodom/internal/torrents"
)

// pass — проверить подписки, у которых подошёл срок: раз в 6 часов, отложенный переход — через 10 минут,
// подписка без списка серий — сразу.
func (m *Module) pass(ctx context.Context) {
	fs, err := m.st.active(ctx)
	if err != nil {
		m.log.Error("подписки не читаются", "err", err)
		return
	}
	now := m.now()
	for _, f := range fs {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		retry, busy := m.retryAt[f.Release]
		m.mu.Unlock()
		if len(f.Paths) > 0 && now.Sub(f.CheckedAt) < m.every && !(busy && !now.Before(retry)) {
			continue
		}
		if err := m.check(ctx, f); err != nil {
			m.log.Warn("подписка не проверилась — повтор при следующей проверке", "release", f.Release, "err", err)
		}
	}
}

// check — страница раздачи заново: новая версия — переход или докачка новых серий и оповещение; снята с
// трекера — подписка останавливается; вышла последняя серия и скачана — подписка заканчивается.
func (m *Module) check(ctx context.Context, f Follow) error {
	now := m.now()
	v, err := m.o.Catalog.CheckRelease(ctx, f.Release)
	if errors.Is(err, source.ErrRemoved) {
		if err := m.st.setState(ctx, f.Release, StateRemoved); err != nil {
			return err
		}
		_, err := m.st.addUpdate(ctx, Update{Release: f.Release, Kind: KindRemoved, Label: "Раздача снята с трекера", At: now})
		return err
	}
	if err := m.st.setChecked(ctx, f.Release, now); err != nil {
		return err
	}
	if err != nil {
		return err // трекер выключен или недоступен — при следующей проверке
	}
	if v.InfoHash == f.InfoHash && len(f.Paths) > 0 {
		return m.finishIfDone(ctx, f)
	}
	raw := v.Torrent
	if raw == nil {
		ictx, cancel := context.WithTimeout(ctx, infoWait)
		raw, err = m.o.Torrents.FetchInfo(ictx, v.Magnet)
		cancel()
		if err != nil {
			return fmt.Errorf("метаинфо версии не пришла от пиров: %w", err)
		}
	}
	files, err := torrents.PlayableFiles(raw)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for _, x := range files {
		paths = append(paths, x.Name)
	}
	_, _, total := meta.Episodes(v.Title)
	if len(f.Paths) == 0 {
		// Список серий подписанной версии (Rutracker без .torrent) — точка отсчёта, без оповещения.
		return m.st.setVersion(ctx, f.Release, v.InfoHash, len(files), total, paths)
	}
	known := map[string]bool{}
	for _, p := range f.Paths {
		known[p] = true
	}
	var fresh []torrents.FileInfo
	for _, x := range files {
		if !known[x.Name] {
			fresh = append(fresh, x)
		}
	}
	download := make([]int, 0, len(fresh))
	for _, x := range fresh {
		download = append(download, x.Index)
	}
	newIH, err := m.apply(ctx, f.InfoHash, raw, download)
	if errors.Is(err, torrents.ErrBusy) {
		m.mu.Lock()
		m.retryAt[f.Release] = now.Add(busyRetry)
		m.mu.Unlock()
		m.log.Info("подписка: раздачу смотрят — переход позже", "release", f.Release)
		return nil
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.retryAt, f.Release)
	m.mu.Unlock()
	if err := m.st.setVersion(ctx, f.Release, newIH.HexString(), len(files), total, paths); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	ufs := make([]UpdateFile, 0, len(fresh))
	for _, x := range fresh {
		ufs = append(ufs, UpdateFile{Index: x.Index, Path: x.Name})
	}
	_, err = m.st.addUpdate(ctx, Update{Release: f.Release, Kind: KindEpisodes, InfoHash: newIH.HexString(), Label: episodeLabel(fresh, v.Title), Files: ufs, At: now})
	return err
}

// apply — новая версия: раздача в «Загрузках» (что-то хранится) — переход со скачанным и докачка новых;
// не качали — новая версия открывается и качаются только новые серии.
func (m *Module) apply(ctx context.Context, oldHex string, raw []byte, download []int) (metainfo.Hash, error) {
	var old metainfo.Hash
	if err := old.FromHexString(oldHex); err == nil {
		if st, ok := m.o.Torrents.Status(old); ok && anyStored(st) {
			return m.o.Torrents.Upgrade(ctx, old, raw, download, m.o.Rekey)
		}
	}
	ih, err := m.o.Torrents.Open(ctx, torrents.Source{Torrent: raw})
	if err != nil || len(download) == 0 {
		return ih, err
	}
	return ih, m.o.Torrents.Download(ctx, ih, download)
}

func anyStored(st torrents.TorrentStatus) bool {
	for _, f := range st.Files {
		if f.Stored {
			return true
		}
	}
	return false
}

// finishIfDone — вышла последняя серия («из N», все N есть) и скачано всё, что качалось, — подписка
// заканчивается (спека 11b, 6.4).
func (m *Module) finishIfDone(ctx context.Context, f Follow) error {
	if f.Total == 0 || f.Episodes < f.Total {
		return nil
	}
	var ih metainfo.Hash
	if err := ih.FromHexString(f.InfoHash); err == nil {
		if st, ok := m.o.Torrents.Status(ih); ok {
			for _, x := range st.Files {
				if x.Stored && x.Done < x.Size {
					return nil
				}
			}
		}
	}
	return m.st.setState(ctx, f.Release, StateFinished)
}

// episodeLabel — «1×07», «1×07–1×08» (подряд), «1×07, 1×09»; номер не разобрать — имя файла.
func episodeLabel(files []torrents.FileInfo, title string) string {
	season := meta.SeasonNumber(meta.ParseTitle(title).Season)
	if season == 0 {
		season = 1
	}
	type ep struct{ s, e int }
	var eps []ep
	for _, f := range files {
		s, e := library.Episode(baseName(f.Name))
		if e == 0 {
			var names []string
			for _, f := range files {
				names = append(names, baseName(f.Name))
			}
			return strings.Join(names, ", ")
		}
		if s == 0 {
			s = season
		}
		eps = append(eps, ep{s, e})
	}
	if len(eps) == 0 {
		return ""
	}
	one := func(x ep) string { return fmt.Sprintf("%d×%02d", x.s, x.e) }
	consecutive := true
	for i := 1; i < len(eps); i++ {
		consecutive = consecutive && eps[i].s == eps[0].s && eps[i].e == eps[i-1].e+1
	}
	if len(eps) > 1 && consecutive {
		return one(eps[0]) + "–" + one(eps[len(eps)-1])
	}
	parts := make([]string, 0, len(eps))
	for _, x := range eps {
		parts = append(parts, one(x))
	}
	return strings.Join(parts, ", ")
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
