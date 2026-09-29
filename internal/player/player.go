// Package player — запуск плеера на ПК и плейлисты (основная спека, раздел 14): файл .m3u8 для
// устройств без Kinodom, ссылка kinodom:// для браузера на этом ПК и её строгий разбор, поиск VLC и
// MPC-HC.
package player

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// M3U — плейлист из одного потока. UTF-8: так кириллицу в названии читает и MPC-HC.
func M3U(title, streamURL string) []byte {
	title = strings.NewReplacer("\r", " ", "\n", " ").Replace(title)
	return []byte("#EXTM3U\n#EXTINF:-1," + title + "\n" + streamURL + "\n")
}

// M3UDisposition — заголовок Content-Disposition: браузер сохраняет файл как «<название>.m3u8»
// (кириллица — по RFC 2231, её понимают все браузеры).
func M3UDisposition(title string) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, title)
	return mime.FormatMediaType("attachment", map[string]string{"filename": name + ".m3u8"})
}

// LaunchURL — ссылка «Открыть в плеере» для браузера на этом ПК: её открывает kinodom open.
func LaunchURL(streamURL, title string) string {
	return "kinodom://play?" + url.Values{"url": {streamURL}, "title": {title}}.Encode()
}

// ErrBadLink — ссылка не та, что делает Kinodom: плеер не запускается.
var ErrBadLink = errors.New("ссылка kinodom:// не от Kinodom — плеер не запущен")

// ParseLaunch разбирает ссылку kinodom://play?url=…&title=… строго по основной спеке (раздел 14):
// поток — только http на 127.0.0.1 или localhost, порт API, путь после path.Clean — /stream/,
// /media/ или /mcast/. Иначе любая страница в браузере могла бы запустить плеер с чем угодно.
func ParseLaunch(link string, apiPort int) (streamURL, title string, err error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "kinodom" || u.Host != "play" || (u.Path != "" && u.Path != "/") {
		return "", "", ErrBadLink
	}
	q := u.Query()
	s, err := url.Parse(q.Get("url"))
	switch {
	case err != nil, s.Scheme != "http", s.User != nil, s.RawQuery != "", s.Fragment != "",
		s.Hostname() != "127.0.0.1" && s.Hostname() != "localhost", s.Port() != strconv.Itoa(apiPort):
		return "", "", ErrBadLink
	}
	clean := path.Clean(s.Path)
	if !strings.HasPrefix(clean, "/stream/") && !strings.HasPrefix(clean, "/media/") && !strings.HasPrefix(clean, "/mcast/") {
		return "", "", fmt.Errorf("%w (путь %s)", ErrBadLink, clean)
	}
	s.Path, s.RawPath = clean, ""
	return s.String(), q.Get("title"), nil
}
