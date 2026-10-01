package catalog

import (
	"testing"
	"time"

	"kinodom/internal/source"
)

// Число скачиваний (план 14Б): пришло — сохраняется; строка без числа (pvc, Rutor) его не затирает;
// страница раздачи обновляет; в карточке — число и дата добавления.
func TestDownloadsKeptWhenUnknown(t *testing.T) {
	db := openDB(t)
	st := catalogStore{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := rel("rutracker", "1", "Кино (2020) WEB-DL", 5, 1, "aa")
	r.Downloads, r.Added = 500, now.Add(-time.Hour)
	ids, err := st.saveFound(ctx, []source.Release{r}, now)
	if err != nil {
		t.Fatal(err)
	}
	r.Downloads = 0
	if _, err := st.saveFound(ctx, []source.Release{r}, now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.rowsByID(ctx, ids)
	if got[ids[0]].Downloads != 500 {
		t.Fatalf("после строки без числа: %d", got[ids[0]].Downloads)
	}
	d := source.Details{Release: r}
	d.Downloads = 839
	if err := st.saveDetails(ctx, ids[0], d, 0, "", "", now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.rowsByID(ctx, ids)
	e := Entry{Downloads: got[ids[0]].Downloads, Added: got[ids[0]].Added}
	if v := e.View(); v.Downloads != 839 || v.Added == nil || !v.Added.Equal(now.Add(-time.Hour)) {
		t.Fatalf("карточка: %+v", v)
	}
	if v := (Entry{}).View(); v.Added != nil {
		t.Fatalf("без даты: %v", v.Added)
	}
}
