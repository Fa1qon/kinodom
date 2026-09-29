package player

import (
	"errors"
	"strings"
	"testing"
)

func TestM3U(t *testing.T) {
	got := string(M3U("Серия 1.\nПобережья", "http://192.168.1.20:8090/stream/ab/0/%D0%A1.mkv"))
	want := "#EXTM3U\n#EXTINF:-1,Серия 1. Побережья\nhttp://192.168.1.20:8090/stream/ab/0/%D0%A1.mkv\n"
	if got != want {
		t.Fatalf("%q", got)
	}
	if d := M3UDisposition(`Доисторическая планета: серия 1/2`); !strings.HasPrefix(d, "attachment; filename*=utf-8''") ||
		!strings.HasSuffix(d, ".m3u8") || strings.Contains(d, "/") {
		t.Fatalf("Content-Disposition: %s", d)
	}
}

// Ссылка «Открыть в плеере» туда и обратно: адрес потока и название с кириллицей не меняются.
func TestLaunchRoundTrip(t *testing.T) {
	stream := "http://127.0.0.1:8090/stream/ab12/3/%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%203.mkv"
	u, title, err := ParseLaunch(LaunchURL(stream, "Серия 3. Пресные воды"), 8090)
	if err != nil || u != stream || title != "Серия 3. Пресные воды" {
		t.Fatalf("%q, %q, %v", u, title, err)
	}
}

// Всё, что Kinodom не делает, — отказ: ссылку kinodom:// может открыть любая страница в браузере
// (основная спека, раздел 14).
func TestParseLaunchRejectsForeignLinks(t *testing.T) {
	bad := map[string]string{
		"другая схема":         "vlc://play?url=http://127.0.0.1:8090/stream/a/0/f.mkv",
		"другой хост ссылки":   "kinodom://open?url=http://127.0.0.1:8090/stream/a/0/f.mkv",
		"путь у ссылки":        "kinodom://play/run?url=http://127.0.0.1:8090/stream/a/0/f.mkv",
		"https":                "kinodom://play?url=https://127.0.0.1:8090/stream/a/0/f.mkv",
		"чужой компьютер":      "kinodom://play?url=http://192.168.1.5:8090/stream/a/0/f.mkv",
		"другой порт":          "kinodom://play?url=http://127.0.0.1:9999/stream/a/0/f.mkv",
		"не поток":             "kinodom://play?url=http://127.0.0.1:8090/api/v1/settings",
		"выход из /stream/":    "kinodom://play?url=http://127.0.0.1:8090/stream/../api/v1/settings",
		"логин в адресе":       "kinodom://play?url=http://u:p@127.0.0.1:8090/stream/a/0/f.mkv",
		"параметры у потока":   "kinodom://play?url=http://127.0.0.1:8090/stream/a/0/f.mkv%3Fx=1",
		"локальный файл":       "kinodom://play?url=file:///C:/Windows/System32/calc.exe",
		"нет адреса":           "kinodom://play?title=x",
		"не ссылка":            `C:\Windows\System32\calc.exe`,
		"javascript":           "kinodom://play?url=javascript:alert(1)",
		"localhost без порта":  "kinodom://play?url=http://localhost/stream/a/0/f.mkv",
		"хост-обманка":         "kinodom://play?url=http://127.0.0.1.evil.example:8090/stream/a/0/f.mkv",
		"закодированный выход": "kinodom://play?url=http://127.0.0.1:8090/stream/%2E%2E/api/v1/settings",
	}
	for name, link := range bad {
		if _, _, err := ParseLaunch(link, 8090); !errors.Is(err, ErrBadLink) {
			t.Errorf("%s: принята (%v)", name, err)
		}
	}
	for _, ok := range []string{
		"kinodom://play?url=http://localhost:8090/media/12",
		"kinodom://play/?url=http://127.0.0.1:8090/mcast/239.0.0.1:1234",
	} {
		if _, _, err := ParseLaunch(ok, 8090); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

// Плеер по настройке: авто — VLC, без VLC — MPC-HC; путь из реестра, которого нет на диске, не
// годится — тогда стандартные папки программ.
func TestFindPlayers(t *testing.T) {
	files := map[string]bool{}
	reg := map[string]string{}
	f := finder{
		reg:    func(key, value string) string { return reg[key+"|"+value] },
		exists: func(p string) bool { return files[p] },
		env: func(k string) string {
			return map[string]string{"ProgramFiles": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`}[k]
		},
	}
	if _, err := f.find("auto"); !errors.Is(err, ErrNoPlayer) {
		t.Fatalf("без плееров: %v", err)
	}
	files[`C:\Program Files (x86)\K-Lite Codec Pack\MPC-HC64\mpc-hc64.exe`] = true
	if p, err := f.find("auto"); err != nil || p.Name != "MPC-HC" {
		t.Fatalf("только MPC-HC: %+v, %v", p, err)
	}
	reg[`HKLM\SOFTWARE\VideoLAN\VLC|`] = `D:\VLC\vlc.exe` // в реестре есть, а файла нет (удалили вручную)
	files[`C:\Program Files\VideoLAN\VLC\vlc.exe`] = true
	if p, err := f.find("auto"); err != nil || p != (Player{"VLC", `C:\Program Files\VideoLAN\VLC\vlc.exe`}) {
		t.Fatalf("авто: %+v, %v", p, err)
	}
	if p, err := f.find("mpc-hc"); err != nil || p.Name != "MPC-HC" {
		t.Fatalf("выбран MPC-HC: %+v, %v", p, err)
	}
	delete(files, `C:\Program Files\VideoLAN\VLC\vlc.exe`)
	if _, err := f.find("vlc"); !errors.Is(err, ErrNoPlayer) {
		t.Fatalf("выбран VLC, а его нет: %v", err)
	}
}

func TestProtocolEntries(t *testing.T) {
	es := ProtocolEntries(`C:\Program Files\Kinodom\kinodom.exe`)
	if es[0].Default != "URL:Kinodom" || es[1].Key != `shell\open\command` ||
		es[1].Default != `"C:\Program Files\Kinodom\kinodom.exe" open "%1"` {
		t.Fatalf("%+v", es)
	}
	if _, ok := es[0].Values["URL Protocol"]; !ok {
		t.Fatal("нет значения URL Protocol — Windows не сочтёт ключ протоколом")
	}
}
