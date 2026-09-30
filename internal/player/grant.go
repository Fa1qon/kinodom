package player

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
)

// GrantURL — ссылка «Разрешить доступ» (спека этапа 11a, раздел 4.7): kinodomw.exe спросит службу,
// её ли это папка, и выдаст учётной записи службы права после окна Windows «Да/Нет».
func GrantURL(path string, apiPort int) string {
	return "kinodom://grant?" + url.Values{"path": {path}, "port": {strconv.Itoa(apiPort)}}.Encode()
}

// ParseGrant разбирает ссылку kinodom://grant строго: только папка на диске этого ПК («D:\…», без
// сетевых и \\?\-путей) и порт API из kinodom.json. Путь — полный, без «..». Какая это папка, решает
// служба: чужую она не подтвердит.
func ParseGrant(link string, apiPort int) (string, error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "kinodom" || u.Host != "grant" || (u.Path != "" && u.Path != "/") {
		return "", ErrBadLink
	}
	q := u.Query()
	if q.Get("port") != strconv.Itoa(apiPort) {
		return "", fmt.Errorf("%w (порт)", ErrBadLink)
	}
	p := q.Get("path")
	if len(p) < 3 || !isDriveLetter(p[0]) || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return "", fmt.Errorf("%w (папка не на диске этого ПК)", ErrBadLink)
	}
	return filepath.Clean(p), nil
}

func isDriveLetter(c byte) bool {
	c |= 0x20 // строчная
	return c >= 'a' && c <= 'z'
}
