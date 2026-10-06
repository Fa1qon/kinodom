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
	"regexp"
	"strconv"
	"strings"
)

// M3U — плейлист из одного потока. UTF-8: так кириллицу в названии читает и MPC-HC.
func M3U(title, streamURL string) []byte {
	return M3UList([]M3UItem{{Title: title, URL: streamURL}})
}

// M3UItem — пункт плейлиста: поток и заголовки, с которыми его открывать.
type M3UItem struct {
	Title     string
	URL       string
	UserAgent string
	Referrer  string
	StartSec  int // открыть с этого места, с; 0 — с начала (спека этапа 8, раздел 7.3)
}

var oneLine = strings.NewReplacer("\r", " ", "\n", " ")

// M3UList — плейлист из нескольких пунктов по порядку: не открылся один — VLC берёт следующий
// (канал IPTV, спека этапа 8, раздел 5.9). Заголовки — строками #EXTVLCOPT.
func M3UList(items []M3UItem) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, it := range items {
		b.WriteString("#EXTINF:-1," + oneLine.Replace(it.Title) + "\n")
		if it.UserAgent != "" {
			b.WriteString("#EXTVLCOPT:http-user-agent=" + oneLine.Replace(it.UserAgent) + "\n")
		}
		if it.Referrer != "" {
			b.WriteString("#EXTVLCOPT:http-referrer=" + oneLine.Replace(it.Referrer) + "\n")
		}
		if it.StartSec > 0 {
			b.WriteString("#EXTVLCOPT:start-time=" + strconv.Itoa(it.StartSec) + "\n")
		}
		b.WriteString(oneLine.Replace(it.URL) + "\n")
	}
	return []byte(b.String())
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
func LaunchURL(streamURL, title string) string { return LaunchURLAt(streamURL, title, 0) }

// LaunchURLAt — то же с местом, откуда открыть (секунды; 0 — с начала).
func LaunchURLAt(streamURL, title string, startSec int) string {
	v := url.Values{"url": {streamURL}, "title": {title}}
	if startSec > 0 {
		v.Set("start", strconv.Itoa(startSec))
	}
	return "kinodom://play?" + v.Encode()
}

// LaunchStart — место из ссылки kinodom:// (секунды); нет или неверное — 0.
func LaunchStart(link string) int {
	u, err := url.Parse(link)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(u.Query().Get("start"))
	if err != nil || n < 0 || n > 7*24*3600 {
		return 0
	}
	return n
}

// ErrBadLink — ссылка не та, что делает Kinodom: плеер не запускается.
var ErrBadLink = errors.New("ссылка kinodom:// не от Kinodom — плеер не запущен")

// ParseLaunch разбирает ссылку kinodom://play?url=…&title=… строго по основной спеке (раздел 14):
// поток — только http на 127.0.0.1 или localhost, порт API, путь после path.Clean — /stream/,
// /media/, /mcast/ или /m3u/ (плейлист канала IPTV — этап 8). Иначе любая страница в браузере
// могла бы запустить плеер с чем угодно. Параметры у адреса запрещены, кроме одного: место в
// плейлисте /m3u/… — ровно start=<целое 0…604800> (медиатека кладёт место в .m3u8, этап 11b).
func ParseLaunch(link string, apiPort int) (streamURL, title string, err error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "kinodom" || u.Host != "play" || (u.Path != "" && u.Path != "/") {
		return "", "", ErrBadLink
	}
	q := u.Query()
	s, err := url.Parse(q.Get("url"))
	switch {
	case err != nil, s.Scheme != "http", s.User != nil, s.Fragment != "",
		s.Hostname() != "127.0.0.1" && s.Hostname() != "localhost", s.Port() != strconv.Itoa(apiPort):
		return "", "", ErrBadLink
	}
	clean := path.Clean(s.Path)
	if !strings.HasPrefix(clean, "/stream/") && !strings.HasPrefix(clean, "/media/") && !strings.HasPrefix(clean, "/mcast/") &&
		!strings.HasPrefix(clean, "/m3u/") {
		return "", "", fmt.Errorf("%w (путь %s)", ErrBadLink, clean)
	}
	if s.RawQuery != "" && (!strings.HasPrefix(clean, "/m3u/") || !startOnly(s.RawQuery)) {
		return "", "", ErrBadLink
	}
	s.Path, s.RawPath = clean, ""
	return s.String(), q.Get("title"), nil
}

// reStartOnly — сырая строка параметров «start=<цифры>» и ничего больше.
var reStartOnly = regexp.MustCompile(`^start=[0-9]{1,6}$`)

// startOnly — параметры адреса — только место в плейлисте 0…604800 с (неделя, как LaunchStart).
func startOnly(rawQuery string) bool {
	if !reStartOnly.MatchString(rawQuery) {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rawQuery, "start="))
	return err == nil && n <= 7*24*3600
}

// ProtocolEntry — ключ реестра ссылки kinodom:// (Windows); на Android записей нет.
type ProtocolEntry struct {
	Key     string
	Default string
	Values  map[string]string
}
