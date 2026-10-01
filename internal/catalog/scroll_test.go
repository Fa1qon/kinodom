package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// portionResult — ответ порции, спрошенной в отдельной горутине (без t: она может висеть на трекере).
type portionResult struct {
	v    ListView
	code int
	took time.Duration
}

func askPortion(h http.Handler, tracker, section string, after int) <-chan portionResult {
	out := make(chan portionResult, 1)
	go func() {
		start := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/catalog?tracker=%s&section=%s&after=%d", tracker, section, after), nil))
		var v ListView
		json.Unmarshal(rec.Body.Bytes(), &v)
		out <- portionResult{v, rec.Code, time.Since(start)}
	}()
	return out
}

func within(t *testing.T, ch <-chan portionResult, d time.Duration, what string) portionResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatalf("%s: нет ответа за %v", what, d)
		return portionResult{}
	}
}

// Замечание № 20 (спека 11b, 15.2): порция отвечает сразу тем, что есть в базе, — запаса меньше порции, и
// следующая страница раздела с трекера качается в фоне. Раньше порция ждала трекер (Rutor — до 77 с на
// страницу, API Rutracker — 90 с на попытку), и под сеткой висело «…».
func TestPortionNotWaitingForTracker(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	block := make(chan struct{})
	rutor.set(func() { rutor.pageBlock = block })
	released := false
	t.Cleanup(func() {
		if !released {
			close(block)
		}
		c.deepWG.Wait()
	})
	next, shown := -1, 0
	for i := 0; shown < 100; i++ {
		r := within(t, askPortion(mux, "rutor", "12", next), 2*time.Second, fmt.Sprintf("порция %d из базы ждёт трекер", i+1))
		if r.code != 200 || len(r.v.Entries) == 0 || !r.v.More {
			t.Fatalf("порция %d: %d, карточек %d, ещё %v", i+1, r.code, len(r.v.Entries), r.v.More)
		}
		shown += len(r.v.Entries)
		next = r.v.Next
	}
	// База кончилась: просьба ждёт подкачку и получает карточки, как только трекер ответил.
	wait := askPortion(mux, "rutor", "12", next)
	select {
	case r := <-wait:
		t.Fatalf("за концом базы ответ без трекера: %d, карточек %d", r.code, len(r.v.Entries))
	case <-time.After(200 * time.Millisecond):
	}
	close(block)
	released = true
	r := within(t, wait, 3*time.Second, "просьба за концом базы")
	if r.code != 200 || len(r.v.Entries) == 0 {
		t.Fatalf("после ответа трекера: %d, карточек %d", r.code, len(r.v.Entries))
	}
}

// Одна страница раздела с трекера за раз (спека 11b, 15.2): фоновая подкачка и две просьбы пульта за концом
// базы — страница запрошена один раз.
func TestDeepPrefetchOnce(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	block := make(chan struct{})
	rutor.set(func() { rutor.pageBlock = block })
	t.Cleanup(func() { c.deepWG.Wait() })
	next, shown := -1, 0
	for shown < 100 {
		r := within(t, askPortion(mux, "rutor", "12", next), 2*time.Second, "порция из базы")
		shown += len(r.v.Entries)
		next = r.v.Next
	}
	a, b := askPortion(mux, "rutor", "12", next), askPortion(mux, "rutor", "12", next)
	time.Sleep(100 * time.Millisecond)
	close(block)
	ra, rb := within(t, a, 3*time.Second, "первая просьба"), within(t, b, 3*time.Second, "вторая просьба")
	if len(ra.v.Entries) == 0 || len(rb.v.Entries) == 0 {
		t.Fatalf("карточек %d и %d", len(ra.v.Entries), len(rb.v.Entries))
	}
	c.deepWG.Wait()
	if n := rutor.Calls("toppage"); n != 1 {
		t.Fatalf("страница раздела запрошена %d раз", n)
	}
}

// Финальное ревью 11b-З: разбор страницы раздела в фоновой подкачке упал паникой — это строка в журнале, а
// не падение всей программы (раньше та же работа шла в обработчике HTTP под защитой от паник).
func TestPrefetchPanicIsLogged(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	rutor.set(func() { rutor.pagePanic = true })
	next, shown := -1, 0
	for shown < 100 {
		r := within(t, askPortion(mux, "rutor", "12", next), 2*time.Second, "порция из базы")
		shown += len(r.v.Entries)
		next = r.v.Next
	}
	c.deepWG.Wait()
	if rutor.Calls("toppage") == 0 {
		t.Fatal("подкачка не запускалась")
	}
}
