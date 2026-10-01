package follow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/catalog"
	"kinodom/internal/source"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/torrents/torrenttest"
)

// version — .torrent версии раздачи «Холод» с сериями files (по 1000 байт).
func version(t *testing.T, files ...string) ([]byte, metainfo.Hash) {
	t.Helper()
	var fs []torrenttest.File
	for _, f := range files {
		fs = append(fs, torrenttest.File{Path: f, Data: bytes.Repeat([]byte(f[len(f)-6:len(f)-4]), 500)})
	}
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Холод", 16<<10, fs...)
	b, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return b, mi.HashInfoBytes()
}

func eps(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, "Holod.S01E0"+strconv.Itoa(i)+".mkv")
	}
	return out
}

type fakeCatalog struct {
	mu       sync.Mutex
	releases map[int64]catalog.Release
	versions map[int64]catalog.Version
	errs     map[int64]error
}

func (f *fakeCatalog) CheckRelease(_ context.Context, id int64) (catalog.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[id]; err != nil {
		return catalog.Version{}, err
	}
	v, ok := f.versions[id]
	if !ok {
		r := f.releases[id]
		return catalog.Version{InfoHash: r.InfoHash, Title: r.Title, Magnet: r.Magnet, Torrent: r.Torrent}, nil
	}
	r := f.releases[id]
	r.InfoHash, r.Title, r.Magnet, r.Torrent = v.InfoHash, v.Title, v.Magnet, v.Torrent
	f.releases[id] = r
	return v, nil
}

func (f *fakeCatalog) Release(_ context.Context, id int64) (catalog.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.releases[id]
	if !ok {
		return catalog.Release{}, catalog.ErrNoRelease
	}
	return r, nil
}

type upgradeCall struct {
	old      metainfo.Hash
	download []int
}

type fakeTorrents struct {
	mu         sync.Mutex
	infos      map[string][]byte // magnet → .torrent
	infoErr    error
	status     map[metainfo.Hash]torrents.TorrentStatus
	upgradeErr error
	upgrades   []upgradeCall
	opened     []metainfo.Hash
	downloads  map[metainfo.Hash][]int
}

func newFakeTorrents() *fakeTorrents {
	return &fakeTorrents{infos: map[string][]byte{}, status: map[metainfo.Hash]torrents.TorrentStatus{}, downloads: map[metainfo.Hash][]int{}}
}

func hashOf(raw []byte) metainfo.Hash {
	mi, _ := metainfo.Load(bytes.NewReader(raw))
	return mi.HashInfoBytes()
}

func (f *fakeTorrents) FetchInfo(_ context.Context, magnet string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	raw, ok := f.infos[magnet]
	if !ok {
		return nil, torrents.ErrNoInfo
	}
	return raw, nil
}

func (f *fakeTorrents) Status(ih metainfo.Hash) (torrents.TorrentStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.status[ih]
	return st, ok
}

func (f *fakeTorrents) Open(_ context.Context, src torrents.Source) (metainfo.Hash, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ih := hashOf(src.Torrent)
	f.opened = append(f.opened, ih)
	return ih, nil
}

func (f *fakeTorrents) Download(_ context.Context, ih metainfo.Hash, files []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads[ih] = files
	return nil
}

func (f *fakeTorrents) Upgrade(_ context.Context, old metainfo.Hash, raw []byte, download []int, _ torrents.Rekey) (metainfo.Hash, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upgradeErr != nil {
		return metainfo.Hash{}, f.upgradeErr
	}
	f.upgrades = append(f.upgrades, upgradeCall{old, download})
	return hashOf(raw), nil
}

type fakeWatched struct {
	mu      sync.Mutex
	watched map[string]bool // "hash/index"
}

func (f *fakeWatched) WatchedAny(_ context.Context, hash string, index int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watched[hash+"/"+strconv.Itoa(index)], nil
}

type fclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fclock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fclock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	db  *store.DB
	cat *fakeCatalog
	tor *fakeTorrents
	w   *fakeWatched
	clk *fclock
	m   *Module
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{db: openDB(t), cat: &fakeCatalog{releases: map[int64]catalog.Release{}, versions: map[int64]catalog.Version{}, errs: map[int64]error{}},
		tor: newFakeTorrents(), w: &fakeWatched{watched: map[string]bool{}}, clk: &fclock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}}
	r.m = New(Options{DB: r.db, Catalog: r.cat, Torrents: r.tor, History: r.w, Now: r.clk.now})
	return r
}

// release — раздача сериала в каталоге: название, версия (.torrent или только magnet).
func (r *rig) release(t *testing.T, title string, raw []byte, ih metainfo.Hash, magnetOnly bool) int64 {
	t.Helper()
	id := addRelease(t, r.db, strconv.Itoa(len(r.cat.releases)+1), title, ih.HexString())
	rel := catalog.Release{Entry: catalog.Entry{ID: id, Tracker: "rutor", Title: title, InfoHash: ih.HexString(), ImageKey: "img1"},
		Magnet: "magnet:?xt=urn:btih:" + ih.HexString()}
	if magnetOnly {
		r.tor.infos[rel.Magnet] = raw
	} else {
		rel.Torrent = raw
	}
	r.cat.releases[id] = rel
	return id
}

// newVersion — на трекере вышла версия raw с названием title.
func (r *rig) newVersion(id int64, title string, raw []byte, ih metainfo.Hash, magnetOnly bool) {
	v := catalog.Version{InfoHash: ih.HexString(), Title: title, Magnet: "magnet:?xt=urn:btih:" + ih.HexString()}
	if magnetOnly {
		r.tor.mu.Lock()
		r.tor.infos[v.Magnet] = raw
		r.tor.mu.Unlock()
	} else {
		v.Torrent = raw
	}
	r.cat.mu.Lock()
	r.cat.versions[id] = v
	r.cat.mu.Unlock()
}

func (r *rig) follow(t *testing.T, id int64) Follow {
	t.Helper()
	f, ok, err := r.m.st.get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("подписки нет: %v %v", ok, err)
	}
	return f
}

// downloaded — раздачу ih скачали: все серии хранятся (и скачаны, если done).
func (r *rig) downloaded(ih metainfo.Hash, n int, done bool) {
	st := torrents.TorrentStatus{}
	for i := 0; i < n; i++ {
		fp := torrents.FileProgress{FileInfo: torrents.FileInfo{Index: i, Name: eps(n)[i], Size: 1000}, Stored: true}
		if done {
			fp.Done, fp.Percent = 1000, 100
		}
		st.Files = append(st.Files, fp)
	}
	r.tor.mu.Lock()
	r.tor.status[ih] = st
	r.tor.mu.Unlock()
}

// В «Загрузках» — переход на новую версию и докачка только новой серии; оповещение «1×07» (спека 11b, 6.2–6.3).
func TestFollowNewEpisodeDownloaded(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
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
	if len(r.tor.upgrades) != 1 || r.tor.upgrades[0].old != h1 || len(r.tor.upgrades[0].download) != 1 || r.tor.upgrades[0].download[0] != 6 {
		t.Fatalf("переход: %+v", r.tor.upgrades)
	}
	f := r.follow(t, id)
	if f.InfoHash != h2.HexString() || f.Episodes != 7 || f.Total != 8 {
		t.Fatalf("подписка: %+v", f)
	}
	us, err := r.m.Updates(ctx)
	if err != nil || len(us) != 1 || us[0].Label != "1×07" || us[0].Hash != h2.HexString() || len(us[0].Files) != 1 || us[0].Files[0].Index != 6 ||
		us[0].Title != "Холод [01-07 из 08] (2026) WEB-DL" || us[0].Name != "Холод" || us[0].ImageKey != "img1" || us[0].Release != id {
		t.Fatalf("оповещение: %+v %v", us, err)
	}
	if n, _ := r.m.Unread(ctx); n != 1 {
		t.Fatalf("непросмотренных %d", n)
	}
}

// Следили, но не качали — новая версия открывается и качаются только новые серии (спека 11b, 6.3).
func TestFollowNotDownloadedOpensNew(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026) WEB-DL", v1, h1, false)
	r.m.Follow(ctx, id)
	v2, h2 := version(t, eps(8)...)
	r.newVersion(id, "Холод [01-08 из 08] (2026) WEB-DL", v2, h2, false)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if len(r.tor.opened) != 1 || r.tor.opened[0] != h2 || len(r.tor.downloads[h2]) != 2 || len(r.tor.upgrades) != 0 {
		t.Fatalf("открыто %v, качается %v, переходов %d", r.tor.opened, r.tor.downloads, len(r.tor.upgrades))
	}
	if us, _ := r.m.Updates(ctx); len(us) != 1 || us[0].Label != "1×07–1×08" {
		t.Fatalf("оповещение: %+v", us)
	}
}

// Rutracker без .torrent: метаинфо новой версии не пришла — ни перехода, ни оповещения, известная версия
// не сдвигается; пришла при следующей проверке — всё как обычно (Review Focus 5).
func TestFollowWaitsForMetainfo(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод (Сезон 1, Серии 1-6 из 8) [2026, WEB-DL]", v1, h1, true)
	if err := r.m.Follow(ctx, id); err != nil {
		t.Fatal(err)
	}
	r.m.pass(ctx) // версия при подписке без списка файлов — список от пиров сразу, без оповещения
	if f := r.follow(t, id); len(f.Paths) != 6 {
		t.Fatalf("список серий подписанной версии: %+v", f)
	}
	v2, h2 := version(t, eps(7)...)
	r.newVersion(id, "Холод (Сезон 1, Серии 1-7 из 8) [2026, WEB-DL]", v2, h2, true)
	r.tor.mu.Lock()
	r.tor.infoErr = torrents.ErrNoInfo
	r.tor.mu.Unlock()
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.InfoHash != h1.HexString() {
		t.Fatalf("метаинфо не пришла, а версия сдвинулась: %+v", f)
	}
	if us, _ := r.m.Updates(ctx); len(us) != 0 {
		t.Fatalf("оповещение без метаинфо: %+v", us)
	}
	r.tor.mu.Lock()
	r.tor.infoErr = nil
	r.tor.mu.Unlock()
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.InfoHash != h2.HexString() {
		t.Fatalf("метаинфо пришла — версия новая: %+v", f)
	}
	if us, _ := r.m.Updates(ctx); len(us) != 1 || us[0].Label != "1×07" {
		t.Fatalf("оповещение: %+v", us)
	}
}

// Раздачу сняли с трекера — подписка останавливается, строка «Раздача снята с трекера».
func TestFollowRemoved(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026)", v1, h1, false)
	r.m.Follow(ctx, id)
	r.cat.errs[id] = source.ErrRemoved
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.State != StateRemoved {
		t.Fatalf("подписка: %+v", f)
	}
	if us, _ := r.m.Updates(ctx); len(us) != 1 || us[0].Kind != KindRemoved {
		t.Fatalf("оповещение: %+v", us)
	}
}

// Вышла последняя серия («из 8», все 8) и она скачана — подписка заканчивается сама (спека 11b, 6.4).
func TestFollowFinished(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(8)...)
	id := r.release(t, "Холод [01-08 из 08] (2026)", v1, h1, false)
	r.downloaded(h1, 8, false)
	r.m.Follow(ctx, id)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.State != StateActive {
		t.Fatalf("последняя серия ещё качается — подписка идёт: %+v", f)
	}
	r.downloaded(h1, 8, true)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.State != StateFinished {
		t.Fatalf("всё вышло и скачано: %+v", f)
	}
}

// Раздачу смотрят — переход откладывается и повторяется через 10 минут, не через 6 часов.
func TestFollowBusyRetries(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026)", v1, h1, false)
	r.downloaded(h1, 6, false)
	r.m.Follow(ctx, id)
	v2, h2 := version(t, eps(7)...)
	r.newVersion(id, "Холод [01-07 из 08] (2026)", v2, h2, false)
	r.tor.upgradeErr = torrents.ErrBusy
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	if f := r.follow(t, id); f.InfoHash != h1.HexString() {
		t.Fatalf("переход отложен, а версия сдвинулась: %+v", f)
	}
	r.tor.mu.Lock()
	r.tor.upgradeErr = nil
	r.tor.mu.Unlock()
	r.clk.add(busyRetry + time.Second)
	r.m.pass(ctx)
	if len(r.tor.upgrades) != 1 || r.follow(t, id).InfoHash != h2.HexString() {
		t.Fatalf("через 10 минут переход не повторился: %+v", r.tor.upgrades)
	}
}

// Новую серию досмотрели на любом устройстве — строка пропадает, колокольчик гаснет.
func TestUnreadClearsWhenWatched(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026)", v1, h1, false)
	r.m.Follow(ctx, id)
	v2, h2 := version(t, eps(7)...)
	r.newVersion(id, "Холод [01-07 из 08] (2026)", v2, h2, false)
	r.clk.add(checkEvery + time.Second)
	r.m.pass(ctx)
	r.w.watched[h2.HexString()+"/6"] = true
	if n, _ := r.m.Unread(ctx); n != 0 {
		t.Fatalf("непросмотренных %d", n)
	}
	if us, _ := r.m.Updates(ctx); len(us) != 0 {
		t.Fatalf("строка осталась: %+v", us)
	}
}

// Фильм — не сериал: «Следить» нельзя.
func TestFollowNotSeries(t *testing.T) {
	r := newRig(t)
	v1, h1 := version(t, "Odyssey.2026.mkv")
	id := r.release(t, "Одиссея / The Odyssey (2026) WEB-DL", v1, h1, false)
	if err := r.m.Follow(context.Background(), id); !errors.Is(err, ErrNotSeries) {
		t.Fatalf("фильм: %v", err)
	}
}

type testRouter struct{ mux *http.ServeMux }

func (t testRouter) Handle(p, _ string, h http.Handler) { t.mux.Handle(p, h) }
func (t testRouter) HandleHome(p, _ string, h http.Handler) {
	t.mux.Handle(p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RemoteAddr != "127.0.0.1:1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	}))
}

func call(mux http.Handler, method, path, from string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = from
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Маршруты (спека 11b, 6.6): «Следить», «Не следить» и «Убрать» — только из домашней сети; список — всем.
func TestFollowRoutesHomeOnly(t *testing.T) {
	r := newRig(t)
	v1, h1 := version(t, eps(6)...)
	id := r.release(t, "Холод [01-06 из 08] (2026)", v1, h1, false)
	mux := http.NewServeMux()
	r.m.Register(testRouter{mux})
	path := "/api/v1/releases/" + strconv.FormatInt(id, 10) + "/follow"
	if c := call(mux, http.MethodPut, path, "203.0.113.5:1").Code; c != http.StatusForbidden {
		t.Fatalf("не из дома: %d", c)
	}
	if c := call(mux, http.MethodPut, path, "127.0.0.1:1").Code; c != http.StatusNoContent {
		t.Fatalf("«Следить»: %d", c)
	}
	if st, _ := r.m.State(context.Background(), id); st != StateActive {
		t.Fatalf("состояние: %q", st)
	}
	if c := call(mux, http.MethodPut, "/api/v1/releases/999/follow", "127.0.0.1:1").Code; c != http.StatusNotFound {
		t.Fatalf("нет раздачи: %d", c)
	}
	rec := call(mux, http.MethodGet, "/api/v1/updates", "203.0.113.5:1")
	var list []UpdateView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("список: %d %s", rec.Code, rec.Body)
	}
	if c := call(mux, http.MethodDelete, path, "127.0.0.1:1").Code; c != http.StatusNoContent {
		t.Fatalf("«Не следить»: %d", c)
	}
	if st, _ := r.m.State(context.Background(), id); st != "" {
		t.Fatalf("после «Не следить»: %q", st)
	}
	_ = sql.ErrNoRows
}
