package follow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents"
)

// rekeyTorrents — как настоящий Upgrade: транзакция с переносом ключей фиксируется, а дальше (перенос
// файлов, добавление новой версии) — сбой, ошибка после фиксации; lost — «пропало питание»: подписка не
// узнала даже infohash новой версии.
type rekeyTorrents struct {
	*fakeTorrents
	db       *sql.DB
	afterErr error
	lost     bool
}

func (f *rekeyTorrents) Upgrade(ctx context.Context, old metainfo.Hash, raw []byte, download []int, rekey torrents.Rekey) (metainfo.Hash, error) {
	newIH := hashOf(raw)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return metainfo.Hash{}, err
	}
	if err := RekeyTx(ctx, tx, old.HexString(), newIH.HexString(), map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5}); err != nil {
		tx.Rollback()
		return metainfo.Hash{}, err
	}
	if err := tx.Commit(); err != nil {
		return metainfo.Hash{}, err
	}
	f.mu.Lock()
	f.upgrades = append(f.upgrades, upgradeCall{old, download})
	f.mu.Unlock()
	if f.lost && f.afterErr != nil {
		return metainfo.Hash{}, f.afterErr
	}
	return newIH, f.afterErr
}

// Переход записан (ключи перенесены), а дальше сбой (файл заняли, мало места, пропало питание) — новая серия
// всё равно приходит в «Новые серии» и в очередь: сразу или следующей проверкой (финальное ревью 11b-В).
func TestFollowUpgradeErrorAfterCommit(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "ошибка", true: "питание"}[lost], func(t *testing.T) { upgradeErrorAfterCommit(t, lost) })
	}
}

func upgradeErrorAfterCommit(t *testing.T, lost bool) {
	r := newRig(t)
	ctx := context.Background()
	rt := &rekeyTorrents{fakeTorrents: r.tor, db: r.db.W, afterErr: errors.New("файл не переносится в папку новой версии"), lost: lost}
	r.m = New(Options{DB: r.db, Catalog: r.cat, Torrents: rt, History: r.w, Now: r.clk.now})
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026) WEB-DL", v1, h1, false)
	r.downloaded(h1, 6, false)
	if err := r.m.Follow(ctx, id); err != nil {
		t.Fatal(err)
	}
	v2, h2 := version(t, eps(7)...)
	r.newVersion(id, "Холод [01-07 из 08] (2026) WEB-DL", v2, h2, false)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	f := r.follow(t, id)
	t.Logf("после неудачного перехода: infohash=%s (новый=%v), paths=%d", f.InfoHash[:8], f.InfoHash == h2.HexString(), len(f.Paths))
	// Сбой починился (перезапуск довёл переход), следующая проверка через 6 часов.
	rt.afterErr = nil
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	us, _ := r.m.Updates(ctx)
	t.Logf("переходов %d, оповещений %d, докачек %v", len(rt.upgrades), len(us), r.tor.downloads)
	if len(us) == 0 {
		t.Errorf("новая серия 1×07 так и не пришла в «Новые серии» (и не поставлена в очередь)")
	}
}

// Раздача с бонусным видео: вышло 7 серий из 8 и трейлер — подписка не заканчивается до 8-й серии: вышедшие
// серии — из названия, а не по числу видеофайлов (финальное ревью 11b-В).
func TestFollowFinishedWithExtraVideo(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	files := append(eps(7), "Holod.Trailer.mkv")
	v1, h1 := version(t, files...)
	id := r.release(t, "Холод [01-07 из 08] (2026) WEB-DL", v1, h1, false)
	if err := r.m.Follow(ctx, id); err != nil {
		t.Fatal(err)
	}
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	f := r.follow(t, id)
	t.Logf("подписка: state=%s episodes=%d total=%d", f.State, f.Episodes, f.Total)
	if f.State != StateActive {
		t.Errorf("вышло 7 из 8, а подписка закончилась: %s", f.State)
	}
}

// Колокольчик опрашивается раз в 15 с с каждого пульта: строки «Новых серий» берут название и картинку
// без Catalog.Release — тот значит «человек открыл раздачу» (догрузка, постер без паузы; ревью 11b-В).
func TestUpdatesWithoutReleaseSideEffects(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026) WEB-DL", v1, h1, false)
	if err := r.m.Follow(ctx, id); err != nil {
		t.Fatal(err)
	}
	v2, h2 := version(t, eps(7)...)
	r.newVersion(id, "Холод [01-07 из 08] (2026) WEB-DL", v2, h2, false)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	r.cat.mu.Lock()
	r.cat.releaseCalls = 0
	r.cat.mu.Unlock()
	us, err := r.m.Updates(ctx)
	if err != nil || len(us) != 1 || us[0].Title != "Холод [01-07 из 08] (2026) WEB-DL" || us[0].ImageKey != "img1" || us[0].Name != "Холод" {
		t.Fatalf("строки: %+v, %v", us, err)
	}
	if n, err := r.m.Unread(ctx); err != nil || n != 1 {
		t.Fatalf("колокольчик: %d, %v", n, err)
	}
	r.cat.mu.Lock()
	defer r.cat.mu.Unlock()
	if r.cat.releaseCalls != 0 {
		t.Fatalf("Catalog.Release при опросе колокольчика: %d", r.cat.releaseCalls)
	}
}
