// Package tray — значок Kinodom в области уведомлений Windows (спека этапа 11a, раздел 5.2): меню
// «Открыть Kinodom» и «Выход», двойной щелчок — пульт.
package tray

import (
	_ "embed"
	"encoding/binary"
	"errors"
)

// Icon — значок Kinodom: иконка логотипа в чёрном квадрате, 16–256 px (assets/logo/cut.py).
//
//go:embed kinodom.ico
var Icon []byte

// pickIcon — картинка из файла .ico ближайшего размера не меньше size (иначе самая большая):
// смещение и длина её данных и её размер.
func pickIcon(ico []byte, size int) (off, n, got int, err error) {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return 0, 0, 0, errors.New("не файл .ico")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	best := -1
	for i := range count {
		e := 6 + 16*i
		if e+16 > len(ico) {
			return 0, 0, 0, errors.New("файл .ico обрезан")
		}
		w := int(ico[e])
		if w == 0 {
			w = 256
		}
		better := best < 0 ||
			(w >= size && (got < size || w < got)) || // не меньше нужного и ближе
			(got < size && w > got) // пока всё меньше нужного — берём больший
		if better {
			best, got = i, w
			n = int(binary.LittleEndian.Uint32(ico[e+8:]))
			off = int(binary.LittleEndian.Uint32(ico[e+12:]))
		}
	}
	if best < 0 || off+n > len(ico) {
		return 0, 0, 0, errors.New("в файле .ico нет картинок")
	}
	return off, n, got, nil
}
