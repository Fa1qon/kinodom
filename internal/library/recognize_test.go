package library

import (
	"context"
	"sync"
	"testing"

	"kinodom/internal/meta"
)

// fakeKP — Кинопоиск для тестов: ответы поиска по ключевому слову, ошибки, счётчик запросов.
type fakeKP struct {
	mu      sync.Mutex
	search  map[string][]meta.Film
	errs    map[string]error
	details map[int]meta.FilmDetails
	calls   []string
	block   chan struct{} // не nil — поиск ждёт, пока канал не закроют (Кинопоиск без токена занят каталогом)
}

func (f *fakeKP) Search(_ context.Context, keyword string, year int) ([]meta.Film, error) {
	f.mu.Lock()
	b := f.block
	f.mu.Unlock()
	if b != nil {
		<-b
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, keyword)
	if err := f.errs[keyword]; err != nil {
		return nil, err
	}
	if err := f.errs["*"]; err != nil {
		return nil, err
	}
	var out []meta.Film
	for _, x := range f.search[keyword] {
		if year == 0 || x.Year == 0 || (x.Year >= year-1 && x.Year <= year+1) { // как yearFrom/yearTo API
			out = append(out, x)
		}
	}
	return out, nil
}

func (f *fakeKP) Details(_ context.Context, id int) (meta.FilmDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "details")
	if err := f.errs["details"]; err != nil {
		return meta.FilmDetails{}, err
	}
	d, ok := f.details[id]
	if !ok {
		return meta.FilmDetails{}, meta.ErrNotFound
	}
	return d, nil
}

func film(id int, ru, orig string, year int, typ string) meta.Film {
	return meta.Film{ID: id, NameRu: ru, NameOrig: orig, Year: year, Type: typ}
}

// Привязка только при одном точном совпадении названия, года ±1 и типа (спека, раздел 5.4).
func TestRecognize(t *testing.T) {
	pandorum := film(1, "Пандорум", "Pandorum", 2009, "FILM")
	andor := film(2, "Андор", "Andor", 2022, "TV_SERIES")
	for _, c := range []struct {
		name      string
		search    map[string][]meta.Film
		errs      map[string]error
		p         Parsed
		alt       []string
		layout    Layout
		checkType bool
		want      Result
	}{
		{"точное русское", map[string][]meta.Film{"Малахит": {film(10, "Малахит", "", 2026, "TV_SERIES")}},
			nil, Parsed{Title: "Малахит", Year: 2026}, nil, LayoutSeries, true, Result{10, StateFound}},
		{"точное оригинальное", map[string][]meta.Film{"In the Hand of Dante": {film(11, "Кодекс Данте", "In the Hand of Dante", 2025, "FILM")}},
			nil, Parsed{Title: "In the Hand of Dante", Year: 2025}, nil, LayoutFilms, true, Result{11, StateFound}},
		{"Andor не Пандорум", map[string][]meta.Film{"Andor": {pandorum, andor}},
			nil, Parsed{Title: "Andor"}, nil, LayoutSeries, true, Result{2, StateFound}},
		{"Андора в выдаче нет — не распознано", map[string][]meta.Film{"Andor": {pandorum}},
			nil, Parsed{Title: "Andor"}, nil, LayoutSeries, true, Result{0, StateUnrecognized}},
		{"два точных — не распознано", map[string][]meta.Film{"Shelter": {film(3, "Убежище", "Shelter", 2026, "FILM"), film(4, "Укрытие", "Shelter", 2026, "FILM")}},
			nil, Parsed{Title: "Shelter", Year: 2026}, nil, LayoutFilms, true, Result{0, StateUnrecognized}},
		{"год ±1", map[string][]meta.Film{"Send Help": {film(5, "На помощь!", "Send Help", 2025, "FILM")}},
			nil, Parsed{Title: "Send Help", Year: 2026}, nil, LayoutFilms, true, Result{5, StateFound}},
		{"год далеко", map[string][]meta.Film{"Send Help": {film(5, "На помощь!", "Send Help", 2023, "FILM")}},
			nil, Parsed{Title: "Send Help", Year: 2026}, nil, LayoutFilms, true, Result{0, StateUnrecognized}},
		{"тип не тот", map[string][]meta.Film{"Pitch": {film(6, "Питч", "Pitch", 2016, "TV_SERIES")}},
			nil, Parsed{Title: "Pitch", Year: 2016}, nil, LayoutFilms, true, Result{0, StateUnrecognized}},
		{"у скачанного тип не проверяется", map[string][]meta.Film{"Pitch": {film(6, "Питч", "Pitch", 2016, "TV_SERIES")}},
			nil, Parsed{Title: "Pitch", Year: 2016}, nil, LayoutFilms, false, Result{6, StateFound}},
		{"транслит с мягким знаком", map[string][]meta.Film{"трудно быть богом": {film(7, "Трудно быть богом", "", 2026, "TV_SERIES")}},
			nil, Parsed{Title: "Trudno byt bogom", Year: 2026}, nil, LayoutSeries, true, Result{7, StateFound}},
		{"метка [kp]", nil, map[string]error{"*": meta.ErrQuota}, Parsed{Title: "x", KP: 123}, nil, LayoutFilms, true, Result{123, StateFound}},
		{"квота — ждать", nil, map[string]error{"*": meta.ErrQuota}, Parsed{Title: "Malahit"}, nil, LayoutSeries, true, Result{0, StateWait}},
		{"500 на варианте — следующий", map[string][]meta.Film{"трудно быть богом": {film(7, "Трудно быть богом", "", 2026, "TV_SERIES")}},
			map[string]error{"трудно быт богом": &meta.ServiceError{Status: 500}}, Parsed{Title: "Trudno byt bogom", Year: 2026}, nil, LayoutSeries, true, Result{7, StateFound}},
		{"все 500 — повторить эту позже", nil, map[string]error{"*": &meta.ServiceError{Status: 500}}, Parsed{Title: "Trudno byt bogom"}, nil, LayoutSeries, true, Result{0, StateRetry}},
		// Вживую 2026-09-30: «Silo» — два точных совпадения («Укрытие» 2023 и «Сило» 2017); транслит
		// «сило» нашёл одно — ложная привязка. Неоднозначность — сразу «Не распознано».
		{"неоднозначно — дальше не искать", map[string][]meta.Film{
			"Silo": {film(20, "Укрытие", "Silo", 2023, "TV_SERIES"), film(21, "Сило", "Silo", 2017, "TV_SERIES")},
			"сило": {film(21, "Сило", "Silo", 2017, "TV_SERIES")}},
			nil, Parsed{Title: "Silo"}, nil, LayoutSeries, true, Result{0, StateUnrecognized}},
		{"другое название раздачи", map[string][]meta.Film{"Холоп 3": {film(8, "Холоп 3", "", 2026, "FILM")}},
			nil, Parsed{Title: "Holop"}, []string{"Холоп 3"}, LayoutFilms, false, Result{8, StateFound}},
	} {
		kp := &fakeKP{search: c.search, errs: c.errs}
		got, err := recognize(context.Background(), kp, c.p, c.alt, c.layout, c.checkType)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v (%v), нужно %+v; запросы %q", c.name, got, err, c.want, kp.calls)
		}
		if c.p.KP > 0 && len(kp.calls) > 0 {
			t.Errorf("%s: с меткой [kp] искать не нужно: %q", c.name, kp.calls)
		}
	}
}

func TestRecognizeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	kp := &fakeKP{errs: map[string]error{"*": context.Canceled}}
	if _, err := recognize(ctx, kp, Parsed{Title: "x"}, nil, LayoutFilms, true); err == nil {
		t.Errorf("отмена должна вернуться ошибкой")
	}
}
