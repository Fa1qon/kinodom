// Package netx — сеть Kinodom. Здесь — разбор адреса прокси из настроек; клиенты
// через прокси и напрямую, зеркала и ограничители появятся на этапе 3.
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
