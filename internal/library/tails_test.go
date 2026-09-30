package library

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// Файл со свежим временем изменения (перезаписали, копируется) — хвост Х14: не выпадает из медиатеки и
// не получает новый номер — на номере держится место в истории.
func TestCopyingFileKeepsID(t *testing.T) {
	e, file, unit, path := withFile(t, "film.mkv", make([]byte, 10))
	fresh := e.clk.now()
	os.Chtimes(path, fresh, fresh)
	e.scan(t)
	id := func() (f, u int64) {
		e.d.R.QueryRow(`SELECT id, unit FROM lib_files`).Scan(&f, &u)
		return
	}
	if f, u := id(); f != file || u != unit {
		t.Fatalf("копирующийся файл: номер %d → %d, единица %d → %d", file, f, unit, u)
	}
	old := fresh.Add(-time.Hour)
	os.Chtimes(path, old, old)
	e.scan(t)
	if f, u := id(); f != file || u != unit {
		t.Fatalf("докопировался: номер %d → %d, единица %d → %d", file, f, unit, u)
	}
}

// Раздачу удалили в «Загрузках», а обхода ещё не было (хвост Х18): «Смотреть» и .m3u8 её файла — 410
// «файла больше нет», а не 200 с неработающей ссылкой; файл папки, удалённый с диска, — так же.
func TestPlayRemovedReleaseGone(t *testing.T) {
	e := newEnv(t)
	e.dl.set(malahit(5))
	e.scan(t)
	var file int64
	e.d.R.QueryRow(`SELECT id FROM lib_files ORDER BY id LIMIT 1`).Scan(&file)
	mux := mediaMux(e.l)
	id := strconv.FormatInt(file, 10)
	if w := get(t, mux, "/api/v1/library/files/"+id+"/play", fromPhone); w.Code != http.StatusOK {
		t.Fatalf("до удаления: %d %s", w.Code, w.Body.String())
	}
	e.dl.set()
	for _, u := range []string{"/api/v1/library/files/" + id + "/play", "/m3u/library/" + id + ".m3u8"} {
		if w := get(t, mux, u, fromPhone); w.Code != http.StatusGone {
			t.Errorf("%s после удаления раздачи: %d %s", u, w.Code, w.Body.String())
		}
	}
	e2, f2, _, path := withFile(t, "film.mkv", make([]byte, 10))
	os.Remove(path)
	if w := get(t, mediaMux(e2.l), "/api/v1/library/files/"+strconv.FormatInt(f2, 10)+"/play", fromPhone); w.Code != http.StatusGone {
		t.Errorf("файл папки удалён с диска: %d", w.Code)
	}
}

// При старте медиатека спрашивает загрузки раньше, чем поднялся торрент-движок (хвост Х21): обход
// повторяется через 30 с, а не через час, — скачанное появляется сразу.
func TestDownloadsDownRetriesSoon(t *testing.T) {
	e := newEnv(t)
	e.dl.err = errors.New("загрузки не работают")
	e.scan(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.l.start(runCtx)
	e.dl.err = nil
	e.dl.set(malahit(5))
	e.clk.add(20 * time.Second)
	e.l.retryDownloads(e.clk.now())
	if e.l.scanState().Running {
		t.Fatal("повтор раньше 30 с")
	}
	e.clk.add(11 * time.Second)
	e.l.retryDownloads(e.clk.now())
	deadline := time.Now().Add(5 * time.Second)
	for e.l.scanState().Running || e.dl.calls < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("обход не повторился: обходов %d", e.dl.calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM lib_units WHERE source = 'torrent'`).Scan(&n)
	if n != 1 {
		t.Errorf("скачанное не появилось после повтора: %d", n)
	}
}

// «Разметить вручную» у скачанной раздачи (хвост Х12): ручное название не перекрывается данными раздачи
// ни на карточке, ни в названии для плеера — и после обхода.
func TestManualTitleKept(t *testing.T) {
	e := newEnv(t)
	e.dl.set(malahit(0))
	e.scan(t)
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	if err := e.l.MarkManual(ctx, unit, "Мой Малахит", 2025); err != nil {
		t.Fatal(err)
	}
	e.scan(t)
	v := e.list(t, "pc", 0)
	if len(v.Cards) != 1 || v.Cards[0].Title != "Мой Малахит" || v.Cards[0].Year != 2025 {
		t.Fatalf("карточка: %+v", v.Cards)
	}
	var file int64
	e.d.R.QueryRow(`SELECT id FROM lib_files WHERE unit = ? ORDER BY id LIMIT 1`, unit).Scan(&file)
	f, err := e.l.d.mediaFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.l.fileTitle(ctx, f); got != "Мой Малахит — 1×01" {
		t.Fatalf("название для плеера: %q", got)
	}
}
