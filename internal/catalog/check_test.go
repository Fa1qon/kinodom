package catalog

import (
	"bytes"
	"errors"
	"testing"

	"kinodom/internal/source"
)

// followFixture — раздача сериала Rutor с загруженной страницей и .torrent первой версии.
func followFixture(t *testing.T) (*Catalog, *fakeSource, int64) {
	t.Helper()
	rutor := newFake("rutor")
	r := rel("rutor", "1", "Холод [01-07 из 08] (2026) WEB-DL 1080p", 50, 1<<30, "h1")
	rutor.top["12"] = []source.Release{r}
	rutor.details["1"] = source.Details{Release: r, Description: "Описание", Magnet: "magnet:?xt=urn:btih:h1"}
	rutor.torrents["1"] = []byte("torrent-v1")
	db := openDB(t)
	c, _ := newCatalog(t, db, func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	return c, rutor, releaseID(t, db, "rutor", "1")
}

// Подписка проверяет страницу раздачи заново (спека 11b, 6.2): вышла новая серия — новый infohash,
// название, magnet и .torrent новой версии; каталог отдаёт уже их.
func TestCheckReleaseNewVersion(t *testing.T) {
	c, rutor, id := followFixture(t)
	r2 := rel("rutor", "1", "Холод [01-08 из 08] (2026) WEB-DL 1080p", 60, 1<<30, "h2")
	rutor.set(func() {
		rutor.details["1"] = source.Details{Release: r2, Description: "Описание", Magnet: "magnet:?xt=urn:btih:h2"}
		rutor.torrents["1"] = []byte("torrent-v2")
	})
	v, err := c.CheckRelease(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if v.InfoHash != "h2" || v.Title != r2.Title || v.Magnet != "magnet:?xt=urn:btih:h2" || !bytes.Equal(v.Torrent, []byte("torrent-v2")) {
		t.Fatalf("версия: %+v", v)
	}
	rel, err := c.Release(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rel.InfoHash != "h2" || rel.Title != r2.Title || rel.Magnet != v.Magnet || !bytes.Equal(rel.Torrent, v.Torrent) {
		t.Fatalf("каталог отдаёт старую версию: %+v", rel.Entry)
	}
}

// План 14А: на странице раздачи нет хэша (Rutracker сменил разметку) — версия по хэшу из списка раздела,
// а не ошибка «нет infohash»: подписка продолжает работать.
func TestCheckReleasePageWithoutHash(t *testing.T) {
	c, rutor, id := followFixture(t)
	rutor.set(func() {
		d := rutor.details["1"]
		d.InfoHash, d.Magnet = "", ""
		rutor.details["1"] = d
	})
	v, err := c.CheckRelease(ctx, id)
	if err != nil || v.InfoHash != "h1" {
		t.Fatalf("версия без хэша на странице: %+v, %v", v, err)
	}
}

// Раздачу сняли с трекера — source.ErrRemoved, раздача помечена снятой.
func TestCheckReleaseRemoved(t *testing.T) {
	c, rutor, id := followFixture(t)
	rutor.set(func() { rutor.detailsErr["1"] = source.ErrRemoved })
	if _, err := c.CheckRelease(ctx, id); !errors.Is(err, source.ErrRemoved) {
		t.Fatalf("нужна source.ErrRemoved: %v", err)
	}
	if _, _, _, _, removed, err := c.st.release(ctx, id); err != nil || !removed {
		t.Fatalf("раздача не помечена снятой: %v %v", removed, err)
	}
}

// Трекер не ответил — ошибка (повтор при следующей проверке), раздача не меняется.
func TestCheckReleaseTrackerDown(t *testing.T) {
	c, rutor, id := followFixture(t)
	rutor.set(func() { rutor.detailsErr["1"] = errors.New("Rutor не отвечает") })
	if _, err := c.CheckRelease(ctx, id); err == nil {
		t.Fatal("трекер не ответил — нужна ошибка")
	}
	rel, _ := c.Release(ctx, id)
	if rel.InfoHash != "h1" || !bytes.Equal(rel.Torrent, []byte("torrent-v1")) {
		t.Fatalf("раздача изменилась: %+v", rel.Entry)
	}
}

// Топ раздела принёс другой infohash — старые magnet и .torrent сброшены, страница догрузится заново:
// иначе «Скачать» открыл бы прежнюю версию (спека 11b, 6.2).
func TestUpsertNewHashResetsTorrent(t *testing.T) {
	c, rutor, id := followFixture(t)
	rutor.set(func() {
		rutor.top["12"] = []source.Release{rel("rutor", "1", "Холод [01-08 из 08] (2026) WEB-DL 1080p", 60, 1<<30, "h2")}
	})
	refresh(t, c, true)
	_, _, magnet, torrent, _, err := c.st.release(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if magnet != "" || torrent != nil {
		t.Fatalf("старая версия осталась: magnet %q, .torrent %q", magnet, torrent)
	}
	rel, _ := c.Release(ctx, id)
	if !rel.DetailsPending || rel.InfoHash != "h2" {
		t.Fatalf("страница не догружается заново: %+v", rel.Entry)
	}
}
