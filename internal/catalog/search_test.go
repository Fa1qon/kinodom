package catalog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
)

// waitSearch опрашивает поиск, как клиент: раз в 20 мс, пока не Complete.
func waitSearch(t *testing.T, c *Catalog, q string) SearchState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := c.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if st.Complete {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Поиск сразу отвечает тем, что есть: Rutor уже ответил, Rutracker ещё ищет. Потом результаты
// объединяются, одинаковый infohash схлопывается, всё — по раздающим (спека, раздел 7).
func TestSearchShowsPartialResultsThenAll(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.search = []source.Release{rel("rutor", "1", "Матрица / The Matrix (1999) BDRip", 40, 1, "same"), rel("rutor", "2", "Матрица времени (2017) HDRip", 5, 1, "x")}
	rt.search = []source.Release{rel("rutracker", "9", "Матрица / The Matrix [1999, BDRip 1080p]", 90, 1, "same")}
	rt.searchBlock = make(chan struct{})
	c, _ := newCatalog(t, openDB(t), nil, rutor, rt)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := c.Search(ctx, "  Матрица ")
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Results) == 2 {
			if st.Complete || st.Trackers["rutracker"] != SearchRunning || st.Trackers["rutor"] != SearchOK {
				t.Fatalf("состояние %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Rutor не показан, пока Rutracker ищет: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(rt.searchBlock)
	st := waitSearch(t, c, "матрица")
	if len(st.Results) != 2 || st.Results[0].Tracker != "rutracker" || st.Results[0].Seeders != 90 || st.Results[0].ID == 0 {
		t.Fatalf("результаты %+v", st.Results)
	}
	if rutor.Calls("search") != 1 || rt.Calls("search") != 1 {
		t.Fatal("повторные запросы того же поиска должны брать идущий")
	}
}

// Одинаковый запрос в течение 30 минут — из кэша; поиск с ошибкой не кэшируется.
func TestSearchCache(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.search = []source.Release{rel("rutor", "1", "Океаны (2009) BDRip", 5, 1, "a")}
	rt.searchErr = errors.New("Rutracker: не заданы логин и пароль — поиск недоступен")
	c, clk := newCatalog(t, openDB(t), nil, rutor, rt)
	st := waitSearch(t, c, "океаны")
	if !strings.Contains(st.Trackers["rutracker"], "логин и пароль") || st.Trackers["rutor"] != SearchOK || len(st.Results) != 1 {
		t.Fatalf("состояние %+v", st)
	}
	clk.add(11 * time.Second) // человек ищет снова
	waitSearch(t, c, "океаны")
	if rutor.Calls("search") != 2 {
		t.Fatalf("поиск с ошибкой взят из кэша: поисков %d", rutor.Calls("search"))
	}
	rt.set(func() { rt.searchErr = nil })
	clk.add(11 * time.Second)
	waitSearch(t, c, "океаны")
	clk.add(11 * time.Second)
	waitSearch(t, c, "океаны")
	if rutor.Calls("search") != 3 {
		t.Fatalf("удачный поиск не взят из кэша: поисков %d", rutor.Calls("search"))
	}
	clk.add(31 * time.Minute)
	waitSearch(t, c, "океаны")
	if rutor.Calls("search") != 4 {
		t.Fatalf("через 30 минут: поисков %d", rutor.Calls("search"))
	}
}

// Трекер не ответил за общий предел — поиск закончен, найденное у других показано.
func TestSearchTimeLimit(t *testing.T) {
	saved := searchLimit
	searchLimit = 200 * time.Millisecond
	t.Cleanup(func() { searchLimit = saved })
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.search = []source.Release{rel("rutor", "1", "Океаны (2009) BDRip", 5, 1, "a")}
	rt.searchBlock = make(chan struct{}) // не закроется
	c, _ := newCatalog(t, openDB(t), nil, rutor, rt)
	st := waitSearch(t, c, "океаны")
	if len(st.Results) != 1 || !strings.Contains(st.Trackers["rutracker"], "не ответил") {
		t.Fatalf("состояние %+v", st)
	}
}

// Найденное сохраняется в базе: карточку можно открыть, а в основной каталог оно не попадает.
func TestSearchDoesNotChangeCatalog(t *testing.T) {
	rutor := newFake("rutor")
	rutor.search = []source.Release{rel("rutor", "5", "Найдено (2020) WEB-DL", 5, 1, "a")}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	st := waitSearch(t, c, "найдено")
	if len(st.Results) != 1 || st.Results[0].ID == 0 {
		t.Fatalf("результаты %+v", st.Results)
	}
	if es := list(t, c, ListOptions{}); len(es) != 0 {
		t.Fatalf("поиск попал в каталог: %+v", es)
	}
	if _, err := c.Search(ctx, "   "); err == nil {
		t.Fatal("пустой запрос без ошибки")
	}
}
