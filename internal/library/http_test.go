package library

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"kinodom/internal/httpx"
	"kinodom/internal/meta"
)

// testRouter — маршруты модуля на ServeMux; «дом» — как у api.Server.
type testRouter struct{ mux *http.ServeMux }

func (t testRouter) Handle(p, _ string, h http.Handler) { t.mux.Handle(p, h) }
func (t testRouter) HandleHome(p, _ string, h http.Handler) {
	t.mux.Handle(p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.FromHome(r) {
			httpx.WriteError(w, http.StatusForbidden, "Изменить можно только из домашней сети")
			return
		}
		h.ServeHTTP(w, r)
	}))
}

const fromOutside = "8.8.8.8:5000"

func call(t *testing.T, mux http.Handler, method, url, from string, body any, out any) int {
	t.Helper()
	var rd bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = *bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, url, &rd)
	r.RemoteAddr = from
	r.Host = "127.0.0.1:8090"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, url, err, w.Body.String())
		}
	}
	return w.Code
}

func routes(e *env) *http.ServeMux {
	mux := http.NewServeMux()
	e.l.Register(testRouter{mux})
	return mux
}

// Список и карточка — с любого устройства; перенос в категорию — только из дома.
func TestHTTPListAndCard(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	mux := routes(e)
	var v ListView
	if code := call(t, mux, "GET", "/api/v1/library", fromOutside, nil, &v); code != 200 || len(v.Cards) != 1 || len(v.Categories) != 2 {
		t.Fatalf("список: %d %+v", code, v)
	}
	var c CardView
	if code := call(t, mux, "GET", "/api/v1/library/cards/kp-10", fromPhone, nil, &c); code != 200 || c.Title != "Ограбление в Лос-Анджелесе" {
		t.Errorf("карточка: %d %+v", code, c)
	}
	if code := call(t, mux, "GET", "/api/v1/library/cards/kp-999", fromPhone, nil, nil); code != 404 {
		t.Errorf("нет карточки: %d", code)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/cards/kp-10", fromOutside, map[string]any{"category": catSeries}, nil); code != 403 {
		t.Errorf("снаружи: %d", code)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/cards/kp-10", fromPhone, map[string]any{"category": catSeries}, nil); code != 204 {
		t.Errorf("перенос: %d", code)
	}
	call(t, mux, "GET", "/api/v1/library?category="+strconv.Itoa(catSeries), fromPhone, nil, &v)
	if len(v.Cards) != 1 {
		t.Errorf("после переноса в «Сериалы»: %+v", v.Cards)
	}
	for _, bad := range []map[string]any{{}, {"category": 999}} {
		if code := call(t, mux, "PUT", "/api/v1/library/cards/kp-10", fromPhone, bad, nil); code != 400 && code != 404 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if code := call(t, mux, "PUT", "/api/v1/library/cards/kp-10", fromPhone, map[string]any{"category": nil}, nil); code != 204 {
		t.Errorf("вернуть: %d", code)
	}
}

// «Не распознано»: ссылка на Кинопоиск, ручная разметка, «Искать снова», вернуть автоматику.
func TestHTTPUnitEdits(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catSeries, "Series", "Gryppa.krovi.2025/e1.mkv")
	e.scan(t)
	mux := routes(e)
	var un []UnrecognizedView
	if code := call(t, mux, "GET", "/api/v1/library/unrecognized", fromPhone, nil, &un); code != 200 || len(un) != 1 || un[0].Title != "Gryppa krovi" {
		t.Fatalf("не распознано: %d %+v", code, un)
	}
	id := strconv.FormatInt(un[0].Unit, 10)
	e.kp.details[1234] = meta.FilmDetails{Film: meta.Film{ID: 1234, NameRu: "Группа крови", Year: 2025, Type: "TV_SERIES"}, Description: "d"}
	for _, bad := range []string{"мусор", "https://example.com/film/1", ""} {
		if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromPhone, map[string]any{"kinopoisk": bad}, nil); code != 400 {
			t.Errorf("ссылка %q: %d", bad, code)
		}
	}
	if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromPhone, map[string]any{"kinopoisk": "https://www.kinopoisk.ru/series/1234/"}, nil); code != 204 {
		t.Fatalf("ссылка: %d", code)
	}
	var c CardView
	if code := call(t, mux, "GET", "/api/v1/library/cards/kp-1234", fromPhone, nil, &c); code != 200 || c.Title != "Группа крови" || c.Description != "d" {
		t.Errorf("после ссылки: %d %+v", code, c)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromPhone, map[string]any{"manual": map[string]any{"title": "Домашнее", "year": 2020}}, nil); code != 204 {
		t.Fatalf("вручную: %d", code)
	}
	if code := call(t, mux, "GET", "/api/v1/library/cards/u-"+id, fromPhone, nil, &c); code != 200 || c.Title != "Домашнее" || c.Year != 2020 {
		t.Errorf("после ручной разметки: %d %+v", code, c)
	}
	e.kp.search["группа крови"] = []meta.Film{{ID: 77, NameRu: "Группа крови", Year: 2025, Type: "TV_SERIES"}}
	e.kp.details[77] = meta.FilmDetails{Film: meta.Film{ID: 77, NameRu: "Группа крови", Year: 2025, Type: "TV_SERIES"}}
	if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromPhone, map[string]any{"reset": true}, nil); code != 204 {
		t.Fatalf("вернуть автоматику: %d", code)
	}
	// Нераспознанная единица — по-прежнему карточка с названием из имени: смотреть её можно.
	if code := call(t, mux, "GET", "/api/v1/library/cards/u-"+id, fromPhone, nil, &c); code != 200 || c.Title != "Gryppa krovi" {
		t.Errorf("после «вернуть автоматику»: %d %+v", code, c)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromPhone, map[string]any{"search": true}, nil); code != 204 {
		t.Fatalf("искать снова: %d", code)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/units/999", fromPhone, map[string]any{"search": true}, nil); code != 404 {
		t.Errorf("нет единицы: %d", code)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/units/"+id, fromOutside, map[string]any{"search": true}, nil); code != 403 {
		t.Errorf("снаружи: %d", code)
	}
}

// Категории: создать, изменить, удалить; стандартную — нельзя; папка проверяется; флажок устройства.
func TestHTTPCategories(t *testing.T) {
	e := newEnv(t)
	mux := routes(e)
	study := mkdir(t, e.root, "Study")
	if code := call(t, mux, "POST", "/api/v1/library/categories", fromPhone,
		CategoryInput{Name: "Обучение", Layout: LayoutSeries, Folders: []string{filepath.Join(e.root, "нет")}}, nil); code != 400 {
		t.Errorf("папки нет: %d", code)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if code := call(t, mux, "POST", "/api/v1/library/categories", fromPhone,
		CategoryInput{Name: "18+", Layout: LayoutFilms, Hidden: true, Folders: []string{study}}, &created); code != 201 || created.ID == 0 {
		t.Fatalf("создать: %d %+v", code, created)
	}
	if code := call(t, mux, "POST", "/api/v1/library/categories", fromPhone,
		CategoryInput{Name: "Ещё", Layout: LayoutFilms, Folders: []string{study}}, nil); code != 409 {
		t.Errorf("та же папка: %d", code)
	}
	id := strconv.FormatInt(created.ID, 10)
	if code := call(t, mux, "PUT", "/api/v1/library/categories/"+id+"/device", fromPhone, nil, nil); code != 204 {
		t.Errorf("показывать на устройстве: %d", code)
	}
	var cs []Category
	call(t, mux, "GET", "/api/v1/library/categories", fromPhone, nil, &cs)
	var other []Category
	call(t, mux, "GET", "/api/v1/library/categories", fromPC, nil, &other)
	if len(cs) != 3 || !cs[2].OnDevice || other[2].OnDevice {
		t.Errorf("флажок устройства: телефон %+v, пк %+v", cs, other)
	}
	if code := call(t, mux, "DELETE", "/api/v1/library/categories/1", fromPhone, nil, nil); code != 409 {
		t.Errorf("удалить «Фильмы»: %d", code)
	}
	if code := call(t, mux, "PUT", "/api/v1/library/categories/"+id, fromPhone, CategoryInput{Name: "Взрослое", Layout: LayoutFilms, Hidden: true, Folders: []string{study}}, nil); code != 204 {
		t.Errorf("изменить: %d", code)
	}
	if code := call(t, mux, "DELETE", "/api/v1/library/categories/"+id, fromPhone, nil, nil); code != 204 {
		t.Errorf("удалить: %d", code)
	}
	if code := call(t, mux, "DELETE", "/api/v1/library/categories/"+id, fromPhone, nil, nil); code != 404 {
		t.Errorf("удалить ещё раз: %d", code)
	}
	if code := call(t, mux, "POST", "/api/v1/library/categories", fromOutside, CategoryInput{Name: "x", Layout: LayoutFilms}, nil); code != 403 {
		t.Errorf("снаружи: %d", code)
	}
}

// Обход по открытию медиатеки — «уже идёт»; постер из папки отдаётся.
func TestHTTPScanAndPoster(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catFilms, "Movies", "Home video/film.mkv", "Home video/folder.jpg")
	e.scan(t)
	mux := routes(e)
	var s ScanState
	if code := call(t, mux, "POST", "/api/v1/library/scan", fromPhone, nil, &s); code != 200 {
		t.Errorf("обход: %d", code)
	}
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	r := httptest.NewRequest("GET", "/api/v1/library/units/"+strconv.FormatInt(unit, 10)+"/poster", nil)
	r.RemoteAddr = fromPhone
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "x" {
		t.Errorf("постер: %d %q", w.Code, w.Body.String())
	}
}
