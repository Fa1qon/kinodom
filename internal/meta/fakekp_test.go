package meta

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const testKey = "test-key-5b"

// fakeKP — фейковый Кинопоиск: API и rating.kinopoisk.ru на одном сервере. Ответы — снятые вживую
// (testdata, исследование, раздел 12) и собранные filmJSON.
//
//	/api/v1/api_keys/{key}   — kp-api-keys.json (ключ не тот — 401)
//	/api/v2.2/films/{id}     — films[id], 301 — образец; иначе 404
//	/api/v2.2/films?imdbId=  — imdb[id], tt0133093 — образец; иначе пустой список
//	/api/v2.2/films?keyword= — кириллица — 500 (так бывает вживую); search[keyword]; иначе пустой список
//	/{id}.xml                — xml[id], 301 — образец; иначе 404
type fakeKP struct {
	*httptest.Server
	mu     sync.Mutex
	films  map[int]string
	imdb   map[string]string
	search map[string]string
	xml    map[int]string
	status int            // не 0 — так отвечает API на всё, кроме лимитов ключа
	hits   map[string]int // «key», «film», «imdb», «search», «xml»
	quota  [2]int         // дневной лимит и израсходовано для ответа лимитов; 0,0 — образец
	last   string         // последний запрос API (путь и параметры)
	keys   string         // образец ответа лимитов
	onFilm func(id int)   // если задан — вызывается посреди запроса /films/{id} (тесты гонок)
	// onSearch — если задан и вернул не 0 — так отвечает поиск по этому ключевому слову.
	onSearch func(keyword string) int
}

func newFakeKP(t *testing.T) *fakeKP {
	t.Helper()
	f := &fakeKP{films: map[int]string{301: sample(t, "kp-film-301.json")},
		imdb:   map[string]string{"tt0133093": sample(t, "kp-films-imdb-tt0133093.json")},
		search: map[string]string{"The Matrix": sample(t, "kp-films-keyword-the-matrix.json")},
		xml:    map[int]string{301: sample(t, "kp-rating-301.xml")},
		keys:   sample(t, "kp-api-keys.json"),
		hits:   map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func sample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// filmJSON — фильм в формате /films/{id} и элементов /films. rating = nil — null.
func filmJSON(id int, ru, orig string, year int, rating any) string {
	r := "null"
	if rating != nil {
		r = fmt.Sprint(rating)
	}
	return fmt.Sprintf(`{"kinopoiskId":%d,"imdbId":null,"nameRu":%q,"nameEn":null,"nameOriginal":%q,"year":%d,"type":"FILM","ratingKinopoisk":%s,"ratingImdb":null}`,
		id, ru, orig, year, r)
}

func itemsJSON(films ...string) string {
	return fmt.Sprintf(`{"total":%d,"totalPages":1,"items":[%s]}`, len(films), strings.Join(films, ","))
}

func (f *fakeKP) Hits(kind string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[kind] }

func (f *fakeKP) Last() string { f.mu.Lock(); defer f.mu.Unlock(); return f.last }

func (f *fakeKP) SetStatus(code int) { f.mu.Lock(); f.status = code; f.mu.Unlock() }

func (f *fakeKP) SetQuota(limit, used int) { f.mu.Lock(); f.quota = [2]int{limit, used}; f.mu.Unlock() }

func (f *fakeKP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if strings.HasSuffix(p, ".xml") {
		f.hits["xml"]++
		id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "/"), ".xml"))
		body, ok := f.xml[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml; charset=Windows-1251")
		fmt.Fprint(w, body)
		return
	}
	f.last = r.URL.RequestURI()
	if r.Header.Get("X-API-KEY") != testKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if key, ok := strings.CutPrefix(p, "/api/v1/api_keys/"); ok {
		f.hits["key"]++
		if key != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.quota != [2]int{} {
			fmt.Fprintf(w, `{"totalQuota":{"value":-1,"used":%d},"dailyQuota":{"value":%d,"used":%d},"accountType":"FREE"}`, f.quota[1], f.quota[0], f.quota[1])
			return
		}
		fmt.Fprint(w, f.keys)
		return
	}
	q := r.URL.Query()
	// Сначала учёт запроса (каждый — платный), потом подменный ответ.
	switch {
	case strings.HasPrefix(p, "/api/v2.2/films/"):
		f.hits["film"]++
	case p == "/api/v2.2/films" && q.Get("imdbId") != "":
		f.hits["imdb"]++
	case p == "/api/v2.2/films" && q.Get("keyword") != "":
		f.hits["search"]++
		f.hits["search:"+q.Get("keyword")]++
		if f.onSearch != nil {
			if code := f.onSearch(q.Get("keyword")); code != 0 {
				w.WriteHeader(code)
				return
			}
		}
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch {
	case strings.HasPrefix(p, "/api/v2.2/films/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/api/v2.2/films/"))
		if f.onFilm != nil {
			f.onFilm(id)
		}
		body, ok := f.films[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	case p == "/api/v2.2/films" && q.Get("imdbId") != "":
		if body, ok := f.imdb[q.Get("imdbId")]; ok {
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprint(w, itemsJSON())
	case p == "/api/v2.2/films" && q.Get("keyword") != "":
		if hasCyrillic(q.Get("keyword")) { // так бывает вживую (исследование, разделы 12–13)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"something went wrong."}`)
			return
		}
		if body, ok := f.search[q.Get("keyword")]; ok {
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprint(w, itemsJSON())
	default:
		http.NotFound(w, r)
	}
}

func newKP(f *fakeKP, key string) *Kinopoisk {
	return NewKinopoisk(KinopoiskOptions{Key: key, APIBase: f.URL, RatingBase: f.URL, Rate: 1000})
}
