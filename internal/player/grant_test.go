package player

import (
	"errors"
	"testing"
)

// «Разрешить доступ» (спека этапа 11a, раздел 4.7): ссылка kinodom://grant — только папка на диске
// этого ПК и порт API из kinodom.json; путь приводится к полному виду.
func TestParseGrant(t *testing.T) {
	link := GrantURL(`D:\Share\Мои фильмы & сериалы`, 8090)
	p, err := ParseGrant(link, 8090)
	if err != nil || p != `D:\Share\Мои фильмы & сериалы` {
		t.Fatalf("туда и обратно: %q, %v (%s)", p, err, link)
	}
	if p, err := ParseGrant(GrantURL(`C:\Users\a\..\b\`, 8090), 8090); err != nil || p != `C:\Users\b` {
		t.Fatalf("путь с ..: %q, %v", p, err)
	}
	bad := map[string]string{
		"сетевая папка":   GrantURL(`\\server\share`, 8090),
		"длинный путь":    GrantURL(`\\?\C:\Windows`, 8090),
		"не полный путь":  GrantURL(`Share\Movies`, 8090),
		"другой порт":     GrantURL(`D:\Share`, 9999),
		"без порта":       "kinodom://grant?path=D:%5CShare",
		"без пути":        "kinodom://grant?port=8090",
		"другое действие": "kinodom://play?path=D:%5CShare&port=8090",
		"путь у ссылки":   "kinodom://grant/x?path=D:%5CShare&port=8090",
		"диск без слэша":  GrantURL(`D:`, 8090),
	}
	for name, link := range bad {
		if _, err := ParseGrant(link, 8090); !errors.Is(err, ErrBadLink) {
			t.Errorf("%s: принята (%v)", name, err)
		}
	}
}
