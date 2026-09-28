// Package netx — сеть Kinodom: разбор прокси из настроек, транспорт через прокси или
// напрямую, клиент трекера с зеркалами, классификацией ответов, повтором и ограничителем
// частоты (спека, раздел 5).
package netx

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseProxy разбирает адрес прокси. Пустая строка — прокси нет (nil, nil).
// Без схемы непонятно, SOCKS это или HTTP, поэтому такая строка отклоняется с подсказкой.
func ParseProxy(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "socks5" && u.Scheme != "http") {
		return nil, fmt.Errorf("адрес прокси %q не понят: укажите его целиком, например socks5://127.0.0.1:1080 или http://127.0.0.1:8080", s)
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("в адресе прокси %q нет порта", s)
	}
	return u, nil
}
