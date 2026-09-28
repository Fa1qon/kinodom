package rutracker

import (
	"errors"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/source/rutracker/rutrackertest"
)

func utf8Page(t *testing.T, name string) []byte {
	t.Helper()
	b, err := decode(rutrackertest.CP1251(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSearch(t *testing.T) {
	rs, err := parseSearch(utf8Page(t, "search-f2076-seeds.raw-cp1251.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 50 {
		t.Fatalf("строк %d, в образце 50", len(rs))
	}
	want := source.Release{Tracker: "rutracker", TopicID: "4215143", CategoryID: "2076",
		Title:   "Космос: Персональное путешествие с Карлом Саганом / Cosmos: A Personal Voyage (Carl Sagan) [1980, научно-популярный, DVDRip-AVC, RUS/ENG]",
		Seeders: 37, Leechers: 12, Size: 14528543148, Added: time.Unix(1400655625, 0)}
	if got := rs[0]; got != want {
		t.Fatalf("первая строка:\n%+v\nнужно\n%+v", got, want)
	}
}

func TestParseSearchGarbage(t *testing.T) {
	for name, body := range map[string]string{
		"страница входа":    string(utf8Page(t, "login-form.dom.html")),
		"строки без ссылок": `<table id="tor-tbl"><tbody><tr class="hl-tr"><td>1</td></tr></tbody></table>`,
	} {
		var pe *source.ParseError
		if _, err := parseSearch([]byte(body)); !errors.As(err, &pe) {
			t.Errorf("%s: ожидалась ParseError, получено %v", name, err)
		}
	}
}

func TestParseTopic(t *testing.T) {
	d, err := parseTopic(utf8Page(t, "topic.src.html"))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct{ field, got, want string }{
		{"Title", d.Title, "Экстрасенсы. Реванш. 3 сезон: 2 выпуск. Выпуск от 26.09.2026. [2026, Паранормальное шоу, мистика, экстрасенсы, сверхъестественное, HDTV 1080p]"},
		{"CategoryID", d.CategoryID, "314"},
		{"InfoHash", d.InfoHash, "5fab7552e56bd8d03766ced4a4b62e0b20fbee9e"},
		{"PosterURL", d.PosterURL, "https://i128.fastpic.org/big/2026/0919/1a/d16803af3e1cc8654ff071da85d0931a.jpg"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, нужно %q", c.field, c.got, c.want)
		}
	}
	if d.Seeders != 134 || d.Leechers != 31 || d.Size != 6108680240 {
		t.Errorf("раздают %d, качают %d, размер %d", d.Seeders, d.Leechers, d.Size)
	}
	if !strings.Contains(d.Description, "Год выпуска: 2026") || strings.Contains(d.Description, "Участники 3 сезона") || strings.Contains(d.Description, "<") {
		t.Errorf("описание (спойлеры и разметка не нужны):\n%s", d.Description)
	}
	if d.Magnet != buildMagnet(d.InfoHash) {
		t.Errorf("magnet %q", d.Magnet)
	}
}

// Гостю страница раздачи видна без блока с размером и раздающими — это не ошибка.
func TestParseTopicAsGuest(t *testing.T) {
	d, err := parseTopic(utf8Page(t, "topic-guest.raw-cp1251.html"))
	if err != nil || d.CategoryID != "85" || d.InfoHash == "" || d.Seeders != 0 || d.Size != 0 {
		t.Fatalf("гостевая раздача: %+v, %v", d.Release, err)
	}
}

func TestParseTopicMissingBlocks(t *testing.T) {
	var pe *source.ParseError
	if _, err := parseTopic(utf8Page(t, "topic-missing-99999999.raw-cp1251.html")); !errors.As(err, &pe) || !strings.Contains(pe.Block, "topic-title") {
		t.Errorf("без названия: %v", err)
	}
	noMagnet := `<a id="topic-title">Фильм</a><div class="post_body">текст</div>`
	if _, err := parseTopic([]byte(noMagnet)); !errors.As(err, &pe) || !strings.Contains(pe.Block, "magnet") {
		t.Errorf("без magnet: %v", err)
	}
}

// Постер — только http(s): javascript: и прочее не годится для скачивания сервером.
func TestParseTopicRejectsOddPosterScheme(t *testing.T) {
	body := `<a id="topic-title">Фильм</a><div class="post_body"><var class="postImg postImgAligned" title="javascript:alert(1)"></var></div>` +
		`<a class="magnet-link" href="magnet:?xt=urn:btih:` + strings.Repeat("ab", 20) + `">m</a>`
	d, err := parseTopic([]byte(body))
	if err != nil || d.PosterURL != "" {
		t.Fatalf("постер %q, %v", d.PosterURL, err)
	}
}

func TestParseLogin(t *testing.T) {
	if err := parseLogin(utf8Page(t, "login-result.dom.html")); err != nil {
		t.Errorf("вход удался, а получено %v", err)
	}
	if err := parseLogin(utf8Page(t, "login-wrong.raw-cp1251.html")); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("неверный пароль: %v", err)
	}
	var pe *source.ParseError
	if err := parseLogin(utf8Page(t, "login-form.dom.html")); !errors.As(err, &pe) {
		t.Errorf("просто форма входа — непонятный итог: %v", err)
	}
}

// Капча без текста ошибки — просьба ввести код (картинка и поля — для пульта, этап 7).
func TestParseLoginCaptcha(t *testing.T) {
	page := strings.Replace(string(utf8Page(t, "login-wrong.raw-cp1251.html")), "неверное/неактивное имя пользователя или неверный пароль", "", 1)
	var ce *CaptchaError
	if err := parseLogin([]byte(page)); !errors.As(err, &ce) {
		t.Fatalf("ожидалась CaptchaError, получено %v", err)
	}
	if ce.ImageURL != "https://static.rutracker.cc/captcha/9bb871d032d1ae76ff4365661bc06b6c.jpg?1726889043" ||
		ce.SID != "laDDKrJaJ7Vgrs2c41st" || ce.CodeField != "cap_code_31cf27f70204da16a65595215ab9a33b" {
		t.Fatalf("капча: %+v", ce)
	}
}

func TestBuildMagnet(t *testing.T) {
	got := buildMagnet("5fab7552e56bd8d03766ced4a4b62e0b20fbee9e")
	want := "magnet:?xt=urn:btih:5fab7552e56bd8d03766ced4a4b62e0b20fbee9e" +
		"&tr=http%3A%2F%2Fbt4.t-ru.org%2Fann%3Fmagnet&tr=http%3A%2F%2Fbt2.t-ru.org%2Fann%3Fmagnet&tr=udp%3A%2F%2Fopentor.net%3A6969"
	if got != want {
		t.Fatalf("magnet:\n%s\nнужно\n%s", got, want)
	}
}
