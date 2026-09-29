package iptv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/probe"
	"kinodom/internal/iptv/xmltv"
)

// updateLoop — раз в минуту (и по сигналу) проверяет, не пора ли скачать телепрограмму, базу
// iptv-org, плейлисты по ссылкам, сдвинуть окно телепрограммы и почистить старые проверки.
func (m *Module) updateLoop(ctx context.Context) error {
	pruned := ""
	for {
		now := m.local()
		m.updateEPG(ctx, now)
		m.updateOrg(ctx, now)
		m.updatePlaylists(ctx, now)
		_, _, day := window(now)
		m.mu.Lock()
		reparse := m.guide != nil && m.guideDay != day
		m.mu.Unlock()
		if reparse { // наступили новые сутки — окно передач сдвигается
			if err := m.loadGuide(m.epgPath()); err != nil {
				m.log.Warn("iptv: телепрограмма не перечиталась", "err", err)
			}
			m.changed()
		}
		if pruned != day {
			if err := m.d.pruneChecks(ctx, now.Add(-keepChecks)); err != nil {
				m.log.Warn("iptv: старые проверки не удалились", "err", err)
			}
			pruned = day
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
		case <-time.After(time.Minute):
		}
	}
}

// download — файл по ссылке напрямую, не больше limit байт.
func (m *Module) download(ctx context.Context, raw string, limit int64, timeout time.Duration) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("нужна ссылка http(s)")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", probe.UserAgent)
	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("нет ответа за %d с", int(timeout.Seconds()))
		}
		return nil, errors.New("не скачивается: " + shortErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер ответил %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("оборвалось скачивание: " + shortErr(err))
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("файл больше %d МБ", limit>>20)
	}
	return b, nil
}

// shortErr — ошибка сети без адреса целиком (в ссылках плейлистов бывают ключи доступа).
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// writeFile — атомарная запись: сначала во временный файл, потом переименование.
func writeFile(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (m *Module) updateEPG(ctx context.Context, now time.Time) {
	m.mu.Lock()
	due := !now.Before(m.epgSrc.next)
	u := m.epgURL
	m.mu.Unlock()
	if !due {
		return
	}
	err := m.fetchEPG(ctx, u)
	done := m.local() // скачивание длится до минут — время обновления по его окончанию
	m.mu.Lock()
	src := &m.epgSrc
	if err == nil {
		src.at, src.next, src.fails, src.err = done, done.Add(epgEvery), 0, ""
	} else {
		src.fails++
		src.next, src.err = done.Add(retryAfter(src.fails)), err.Error()
	}
	s := *src
	m.mu.Unlock()
	if err != nil {
		m.log.Warn("iptv: телепрограмма не скачалась", "err", err)
	}
	m.problem(ctx, "iptv.epg", s, staleAfter, "Телепрограмма не обновляется: ")
	if err == nil {
		m.changed()
	}
}

// fetchEPG — скачать, проверить разбором и только потом заменить файл.
func (m *Module) fetchEPG(ctx context.Context, u string) error {
	b, err := m.download(ctx, u, maxEPG, 5*time.Minute)
	if err != nil {
		return err
	}
	from, to, day := window(m.local())
	g, err := xmltv.Parse(bytes.NewReader(b), from, to)
	if err != nil {
		return err
	}
	if len(g.Channels) == 0 {
		return errors.New("в телепрограмме нет каналов")
	}
	if err := writeFile(m.epgPath(), b); err != nil {
		return fmt.Errorf("не сохранилась: %w", err)
	}
	ix := newEPGIndex(g)
	m.mu.Lock()
	m.guide, m.epg, m.guideDay = g, ix, day
	m.mu.Unlock()
	return nil
}

func (m *Module) updateOrg(ctx context.Context, now time.Time) {
	m.mu.Lock()
	due := !now.Before(m.orgSrc.next)
	m.mu.Unlock()
	if !due {
		return
	}
	err := m.fetchOrg(ctx)
	done := m.local()
	m.mu.Lock()
	src := &m.orgSrc
	if err == nil {
		src.at, src.next, src.fails, src.err = done, done.Add(orgEvery), 0, ""
	} else {
		src.fails++
		src.next, src.err = done.Add(retryAfter(src.fails)), err.Error()
	}
	s := *src
	m.mu.Unlock()
	if err != nil {
		m.log.Warn("iptv: база iptv-org не скачалась", "err", err)
	}
	m.problem(ctx, "iptv.iptvorg", s, orgStaleAfter, "База каналов iptv-org не обновляется: ")
	if err == nil {
		m.changed()
	}
}

func (m *Module) fetchOrg(ctx context.Context) error {
	base := strings.TrimSuffix(m.o.OrgBase, "/")
	cb, err := m.download(ctx, base+"/channels.json", maxOrg, 3*time.Minute)
	if err != nil {
		return err
	}
	fb, err := m.download(ctx, base+"/feeds.json", maxOrg, 3*time.Minute)
	if err != nil {
		return err
	}
	b, err := labels.LoadBase(bytes.NewReader(cb), bytes.NewReader(fb))
	if err != nil {
		return err
	}
	cp, fp := m.orgPaths()
	if err := writeFile(cp, cb); err != nil {
		return err
	}
	if err := writeFile(fp, fb); err != nil {
		return err
	}
	m.mu.Lock()
	m.base = b
	m.mu.Unlock()
	return nil
}

// problem — проблема в «Состоянии», если последняя попытка не удалась и удачной не было дольше stale
// (или не было совсем).
func (m *Module) problem(ctx context.Context, id string, s source, stale time.Duration, prefix string) {
	var err error
	if s.err != "" && (s.at.IsZero() || m.now().Sub(s.at) > stale) {
		err = m.d.SetProblem(ctx, id, prefix+s.err)
	} else if s.err == "" {
		err = m.d.ClearProblem(ctx, id)
	}
	if err != nil {
		m.log.Error("iptv: проблема не записалась", "id", id, "err", err)
	}
}

// updatePlaylists — плейлисты по ссылкам раз в сутки.
func (m *Module) updatePlaylists(ctx context.Context, now time.Time) {
	m.mu.Lock()
	var due []int64
	for id, pl := range m.pool.playlists {
		if pl.URL == "" {
			continue
		}
		next, ok := m.plNext[id]
		if !ok {
			next = pl.UpdatedAt.Add(playlistEvery)
		}
		if !now.Before(next) {
			due = append(due, id)
		}
	}
	m.mu.Unlock()
	for _, id := range due {
		if err := m.refresh(ctx, id, nil); err != nil && !errors.Is(err, errNoPlaylist) {
			m.log.Warn("iptv: плейлист не обновился", "playlist", id, "err", err)
		}
	}
}

// ErrBadPlaylist — в плейлисте нет ни одного потока, который можно смотреть.
var ErrBadPlaylist = errors.New("в плейлисте нет ни одного потока http(s)")

// PlaylistInput — плейлист из пульта: ссылка или файл.
type PlaylistInput struct {
	Name    string
	URL     string
	Data    []byte
	Limited bool
}

// PlaylistResult — что вышло из добавления или обновления.
type PlaylistResult struct {
	ID          int64 `json:"id"`
	Entries     int   `json:"entries"`
	Unsupported int   `json:"unsupported"`
	Recognized  int   `json:"recognized"`
}

// AddPlaylist — добавить плейлист (спека этапа 8, раздел 5.1): ссылка скачивается сразу, записи
// разбираются, новые источники проверяются в фоне.
func (m *Module) AddPlaylist(ctx context.Context, in PlaylistInput) (PlaylistResult, error) {
	in.URL = strings.TrimSpace(in.URL)
	if (in.URL == "") == (len(in.Data) == 0) {
		return PlaylistResult{}, errors.New("нужна ссылка или файл")
	}
	body := in.Data
	if in.URL != "" {
		b, err := m.download(ctx, in.URL, maxPlaylist, time.Minute)
		if err != nil {
			return PlaylistResult{}, err
		}
		body = b
	}
	if len(body) > maxPlaylist {
		return PlaylistResult{}, fmt.Errorf("файл больше %d МБ", maxPlaylist>>20)
	}
	parsed := m3u.Parse(body)
	if len(parsed.Entries) == 0 {
		return PlaylistResult{}, ErrBadPlaylist
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = nameFromURL(in.URL)
	}
	now := m.now()
	pl := &Playlist{Name: name, URL: in.URL, Limited: in.Limited, AddedAt: now}
	if err := m.d.insertPlaylist(ctx, pl); err != nil {
		return PlaylistResult{}, err
	}
	return m.apply(ctx, pl, parsed, now)
}

// apply — записи плейлиста в пул, пересборка и проверка новых источников.
func (m *Module) apply(ctx context.Context, pl *Playlist, parsed m3u.Playlist, now time.Time) (PlaylistResult, error) {
	if _, _, err := m.d.replaceEntries(ctx, pl.ID, parsed.Entries); err != nil {
		return PlaylistResult{}, err
	}
	pl.UpdatedAt, pl.TriedAt, pl.Error, pl.Unsupported = now, now, "", parsed.Unsupported
	if err := m.d.savePlaylist(ctx, pl); err != nil {
		return PlaylistResult{}, err
	}
	if err := m.reload(ctx); err != nil {
		return PlaylistResult{}, err
	}
	m.mu.Lock()
	delete(m.plFails, pl.ID)
	m.plNext[pl.ID] = now.Add(playlistEvery)
	m.mu.Unlock()
	m.clearPlaylistProblem(ctx, pl.ID)
	m.rebuild(ctx)
	m.poke(m.lightNew)
	res := PlaylistResult{ID: pl.ID, Entries: len(parsed.Entries), Unsupported: parsed.Unsupported}
	l := m.Lineup()
	m.mu.Lock()
	for _, s := range m.pool.streams {
		for _, e := range s.Entries {
			if e.Playlist == pl.ID {
				if _, ok := l.StreamChannel[s.ID]; ok {
					res.Recognized++
				}
				break
			}
		}
	}
	m.mu.Unlock()
	return res, nil
}

// reload — пул заново из базы (после изменения плейлистов). Состояния источников в базе те же.
func (m *Module) reload(ctx context.Context) error {
	p, err := m.d.load(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.pool = p
	m.mu.Unlock()
	return nil
}

func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "Плейлист"
	}
	base := strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
	if base == "" || base == "." || base == "/" {
		return u.Hostname()
	}
	return base
}

// refresh — обновить плейлист по ссылке (data == nil) или новым файлом.
func (m *Module) refresh(ctx context.Context, id int64, data []byte) error {
	m.mu.Lock()
	cur, ok := m.pool.playlists[id]
	var pl Playlist
	if ok {
		pl = *cur
	}
	m.mu.Unlock()
	if !ok {
		return errNoPlaylist
	}
	now := m.now()
	body := data
	var err error
	if body == nil {
		if pl.URL == "" {
			return errors.New("плейлист загружен файлом — загрузите файл заново")
		}
		body, err = m.download(ctx, pl.URL, maxPlaylist, time.Minute)
	}
	var parsed m3u.Playlist
	if err == nil {
		parsed = m3u.Parse(body)
		if len(parsed.Entries) == 0 {
			err = ErrBadPlaylist
		}
	}
	if err != nil {
		pl.TriedAt, pl.Error = now, err.Error()
		m.mu.Lock()
		m.plFails[id]++
		fails := m.plFails[id]
		m.plNext[id] = now.Add(retryAfter(fails))
		if p := m.pool.playlists[id]; p != nil {
			p.TriedAt, p.Error = pl.TriedAt, pl.Error
		}
		m.mu.Unlock()
		if serr := m.d.savePlaylist(ctx, &pl); serr != nil {
			m.log.Warn("iptv: плейлист не записался", "err", serr)
		}
		if pl.UpdatedAt.IsZero() || now.Sub(pl.UpdatedAt) > staleAfter {
			if perr := m.d.SetProblem(ctx, playlistProblem(id), "Плейлист «"+pl.Name+"» не обновляется: "+err.Error()); perr != nil {
				m.log.Error("iptv: проблема не записалась", "err", perr)
			}
		}
		return err
	}
	_, err = m.apply(ctx, &pl, parsed, now)
	return err
}

func playlistProblem(id int64) string { return "iptv.playlist." + strconv.FormatInt(id, 10) }

func (m *Module) clearPlaylistProblem(ctx context.Context, id int64) {
	if err := m.d.ClearProblem(ctx, playlistProblem(id)); err != nil {
		m.log.Error("iptv: проблема не снялась", "err", err)
	}
}

// RefreshPlaylist — «Обновить»: плейлист по ссылке скачивается сейчас.
func (m *Module) RefreshPlaylist(ctx context.Context, id int64) error {
	return m.refresh(ctx, id, nil)
}

// PlaylistPatch — изменения плейлиста из пульта: только присланные поля.
type PlaylistPatch struct {
	Name    *string
	Limited *bool
	Data    []byte // новый файл
}

// UpdatePlaylist — имя, «ограничено», новый файл.
func (m *Module) UpdatePlaylist(ctx context.Context, id int64, p PlaylistPatch) (PlaylistResult, error) {
	m.mu.Lock()
	cur, ok := m.pool.playlists[id]
	var pl Playlist
	if ok {
		pl = *cur
	}
	m.mu.Unlock()
	if !ok {
		return PlaylistResult{}, errNoPlaylist
	}
	if p.Name != nil {
		if n := strings.TrimSpace(*p.Name); n != "" {
			pl.Name = n
		}
	}
	unlimited := p.Limited != nil && pl.Limited && !*p.Limited
	if p.Limited != nil {
		pl.Limited = *p.Limited
	}
	if err := m.d.savePlaylist(ctx, &pl); err != nil {
		return PlaylistResult{}, err
	}
	m.mu.Lock()
	if cur := m.pool.playlists[id]; cur != nil {
		cur.Name, cur.Limited = pl.Name, pl.Limited
	}
	m.mu.Unlock()
	if p.Data != nil {
		parsed := m3u.Parse(p.Data)
		if len(parsed.Entries) == 0 {
			return PlaylistResult{}, ErrBadPlaylist
		}
		return m.apply(ctx, &pl, parsed, m.now())
	}
	m.rebuild(ctx)
	if unlimited { // источники плейлиста теперь проверяются в фоне — новые сразу
		m.poke(m.lightNew)
	}
	return PlaylistResult{ID: id}, nil
}

// DeletePlaylist — удалить плейлист; источники без записей уходят.
func (m *Module) DeletePlaylist(ctx context.Context, id int64) error {
	if _, err := m.d.deletePlaylist(ctx, id); err != nil {
		return err
	}
	if err := m.reload(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.plFails, id)
	delete(m.plNext, id)
	m.mu.Unlock()
	m.clearPlaylistProblem(ctx, id)
	m.rebuild(ctx)
	return nil
}
