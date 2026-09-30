package source

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode"

	"kinodom/internal/netx"
)

// ErrNotConfigured — адрес трекера не введён: трекер выключен, запросов к нему нет (спека этапа
// 11a, раздел 6). Та же ошибка, что у netx.
var ErrNotConfigured = netx.ErrNotConfigured

// SiteAddress приводит введённый человеком адрес сайта к виду «схема://хост»: схемы нет — https,
// «www.» и путь отбрасываются, хост — в нижнем регистре. Не адрес сайта — ошибка для поля настроек.
func SiteAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("пустой адрес")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("не адрес сайта")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("адрес сайта начинается с http:// или https://")
	}
	if u.User != nil {
		return "", errors.New("адрес сайта без логина и пароля")
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if !validHost(host) {
		return "", errors.New("не адрес сайта")
	}
	if p := u.Port(); p != "" {
		host = net.JoinHostPort(host, p)
	}
	return scheme + "://" + host, nil
}

// validHost — имя сайта с точкой (или IP): буквы, цифры, дефис; без пробелов и пустых частей.
func validHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

// RutorDownload — адрес .torrent Rutor по адресу сайта: «d.<хост>». У адреса-IP — сам адрес.
// Пустой или неверный адрес — "".
func RutorDownload(site string) string {
	u, ip, ok := parseSite(site)
	if !ok {
		return ""
	}
	if ip {
		return site
	}
	return u.Scheme + "://d." + u.Host
}

// RutrackerService — адреса API и ленты Rutracker по адресу сайта: «api.<имя>.cc» и
// «feed.<имя>.cc», где <имя> — первая часть хоста. У адреса-IP — сам адрес.
func RutrackerService(site string) (api, feed string) {
	u, ip, ok := parseSite(site)
	if !ok {
		return "", ""
	}
	if ip {
		return site, site
	}
	name, _, _ := strings.Cut(u.Hostname(), ".")
	return u.Scheme + "://api." + name + ".cc", u.Scheme + "://feed." + name + ".cc"
}

func parseSite(site string) (u *url.URL, ip, ok bool) {
	if site == "" {
		return nil, false, false
	}
	u, err := url.Parse(site)
	if err != nil || u.Host == "" {
		return nil, false, false
	}
	return u, net.ParseIP(u.Hostname()) != nil, true
}
