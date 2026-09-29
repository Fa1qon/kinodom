package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// Кэш картинок чистится по правилам каталога: постеры раздач в каталоге и тронутых за 30 дней
// остаются; картинка давно выпавшей раздачи удаляется, и у раздачи больше нет постера.
func TestSweepImagesKeepsCatalogPosters(t *testing.T) {
	dir := t.TempDir()
	images, err := meta.NewImages(meta.ImagesOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "В каталоге (2020) BDRip", 10, 1, "a")}
	db := openDB(t)
	c, clk := newCatalog(t, db, func(o *Options) { o.Images = images }, rutor)
	refresh(t, c, true)
	inCat, old, recent := meta.ImageKey("http://h/1.jpg"), meta.ImageKey("http://h/2.jpg"), meta.ImageKey("http://h/3.jpg")
	now := clk.now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.W.Exec(`UPDATE releases SET image_key = ? WHERE topic_id = '1'`, inCat)
	must(err)
	_, err = db.W.Exec(`INSERT INTO releases(tracker, topic_id, image_key, updated_at) VALUES
		('rutor', '2', ?, ?), ('rutor', '3', ?, ?)`, old, now.Add(-40*24*time.Hour).UnixMilli(), recent, now.Add(-10*24*time.Hour).UnixMilli())
	must(err)
	for _, k := range []string{inCat, old, recent} {
		p := filepath.Join(dir, k+".jpg")
		must(os.WriteFile(p, []byte("x"), 0o644))
		must(os.Chtimes(p, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))
	}
	must(c.sweepImages(ctx))
	for k, want := range map[string]bool{inCat: true, old: false, recent: true} {
		if _, err := os.Stat(filepath.Join(dir, k+".jpg")); (err == nil) != want {
			t.Errorf("%s: есть = %v, ожидалось %v", k, err == nil, want)
		}
	}
	var key string
	db.R.QueryRow(`SELECT image_key FROM releases WHERE topic_id = '2'`).Scan(&key)
	if key != "" {
		t.Fatalf("у раздачи остался ключ удалённой картинки %q", key)
	}
}
