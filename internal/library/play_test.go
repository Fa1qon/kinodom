package library

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kinodom/internal/playback"
)

func TestPlaySourceSeries(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catSeries, "Series", "Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv", "Show/Show.S02E01.mkv")
	for _, p := range []string{"Show/Show.S01E01.rus.srt", "Show/Subs/Show.S01E01.eng.ass", "Show/Show.S01E01.rus.sup", "Show/Show.S01E02.rus.srt"} {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("1"), 0o644)
	}
	e.scan(t)
	id := func(season, ep int) int64 {
		var x int64
		e.d.R.QueryRow(`SELECT id FROM lib_files WHERE season = ? AND episode = ?`, season, ep).Scan(&x)
		return x
	}
	r := httptest.NewRequest("GET", "/api/v1/play/x", nil)
	r.Host, r.RemoteAddr = "127.0.0.1:8090", fromPhone
	src, err := e.l.PlaySource(r, id(1, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src.Title, "1×01") || !strings.HasPrefix(src.Path, "/media/"+strconv.FormatInt(id(1, 1), 10)+"/") ||
		src.Next == nil || src.Next.Kind != "library" || src.Next.File != id(1, 2) || !strings.Contains(src.NextTitle, "1×02") || src.Launch != nil {
		t.Errorf("1×01: %+v", src)
	}
	if len(src.SubFiles) != 2 || !strings.HasSuffix(src.SubFiles[0], "Show.S01E01.rus.srt") || !strings.HasSuffix(src.SubFiles[1], filepath.Join("Subs", "Show.S01E01.eng.ass")) {
		t.Errorf("субтитры рядом (без .sup и чужой серии): %v", src.SubFiles)
	}
	if src, _ := e.l.PlaySource(r, id(1, 2), false); src.Next != nil {
		t.Errorf("1×02 → следующий сезон: %+v", src.Next)
	}
	var se *playback.StatusError
	if _, err := e.l.PlaySource(r, 999, false); !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("нет файла: %v", err)
	}
}

func TestPlaySourceStart(t *testing.T) {
	e, file, unit, _ := withFile(t, "film.mkv", make([]byte, 10))
	e.hist.SetPosition(ctx, "pc", "lib-"+strconv.FormatInt(unit, 10), int(file), 700, 5400)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Host, r.RemoteAddr = "127.0.0.1:8090", fromPC
	if src, err := e.l.PlaySource(r, file, false); err != nil || src.StartSec != 690 || !strings.HasSuffix(src.M3U, "?start=690") || src.Launch == nil {
		t.Errorf("с места на ПК: %+v %v", src, err)
	}
	if src, _ := e.l.PlaySource(r, file, true); src.StartSec != 0 {
		t.Errorf("с начала: %+v", src)
	}
}
