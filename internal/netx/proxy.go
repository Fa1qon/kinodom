// Package netx — сеть Kinodom: разбор прокси из настроек, транспорт через прокси или
// напрямую, клиент трекера с зеркалами, классификацией ответов, повтором и ограничителем
// частоты (спека, раздел 5).
package netx

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Proxy — прокси для трекеров из настроек, один на приложение: транспорты спрашивают его при
// каждом новом соединении, поэтому смена в пульте действует сразу, без перезапуска модулей (спека
// этапа 7, раздел 5.2). nil — напрямую.
type Proxy struct {
	mu  sync.Mutex
	u   *url.URL          // nil — напрямую
	trs []*http.Transport // при смене прокси их простаивающие соединения закрываются
}

// NewProxy — прокси по строке из настроек; пусто — напрямую.
func NewProxy(s string) (*Proxy, error) {
	u, err := ParseProxy(s)
	if err != nil {
		return nil, err
	}
	return &Proxy{u: u}, nil
}

// Set меняет прокси. Неверный адрес — ошибка, прежний прокси остаётся. Простаивающие соединения
// через старый прокси закрываются: следующий запрос уйдёт уже через новый.
func (p *Proxy) Set(s string) error {
	u, err := ParseProxy(s)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.u = u
	trs := slices.Clone(p.trs)
	p.mu.Unlock()
	for _, t := range trs {
		t.CloseIdleConnections()
	}
	return nil
}

// URL — текущий прокси (копия); nil — напрямую, в том числе у nil-прокси.
func (p *Proxy) URL() *url.URL {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.u == nil {
		return nil
	}
	c := *p.u
	return &c
}

// ForRequest — прокси для запроса: подходит для http.Transport.Proxy и HTTPProxy торрент-движка.
func (p *Proxy) ForRequest(*http.Request) (*url.URL, error) { return p.URL(), nil }

func (p *Proxy) track(t *http.Transport) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.trs = append(p.trs, t)
	p.mu.Unlock()
}

// ParseProxy разбирает адрес прокси. Пустая строка — прокси нет (nil, nil).
// Без схемы непонятно, SOCKS это или HTTP, поэтому такая строка отклоняется с подсказкой.
func ParseProxy(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "socks5" && u.Scheme != "http") {
		return nil, fmt.Errorf("адрес прокси %q не понят: укажите его целиком, например socks5://127.0.0.1:1080 или http://127.0.0.1:8080", redact(s))
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("в адресе прокси %q нет порта", redact(s))
	}
	return u, nil
}

// redact прячет логин и пароль в адресе прокси: текст ошибки попадает в журнал и в «Состояние»,
// а секреты туда не пишутся (спека, раздел 4).
func redact(s string) string {
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return s
	}
	start := 0
	if i := strings.Index(s, "://"); i >= 0 && i < at {
		start = i + len("://")
	}
	return s[:start] + "***" + s[at:]
}
