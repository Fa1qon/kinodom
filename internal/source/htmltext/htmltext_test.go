package htmltext

import (
	"regexp"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func sel(t *testing.T, html string) *goquery.Selection {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	return doc.Find("#x")
}

func TestText(t *testing.T) {
	s := sel(t, `<div id="x"><b>Год:</b>  1999<br>
		<span>Жанр:</span> драма<div class="spoiler">скриншоты</div><div>Описание:<br><br><br>Текст</div></div>`)
	got := Text(s, "div.spoiler")
	want := "Год: 1999\nЖанр: драма\nОписание:\n\nТекст"
	if got != want {
		t.Fatalf("Text:\n%q\nнужно\n%q", got, want)
	}
}

func TestClean(t *testing.T) {
	if got := Clean("  3.87 GB \n x "); got != "3.87 GB x" {
		t.Fatalf("Clean: %q", got)
	}
}

func TestFirstIntAndMatch(t *testing.T) {
	if FirstInt("Сиды:  134") != 134 || FirstInt("нет") != 0 {
		t.Fatal("FirstInt")
	}
	s := sel(t, `<div id="x"><a href="/a">a</a><a href="https://www.kinopoisk.ru/film/301/">к</a></div>`)
	if got := FirstMatch(regexp.MustCompile(`film/(\d+)`), s.Find("a"), "href"); got != "301" {
		t.Fatalf("FirstMatch: %q", got)
	}
}
