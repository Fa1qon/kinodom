package rutracker

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"kinodom/internal/source"
	"kinodom/internal/source/htmltext"
)

// ErrWrongPassword — Rutracker отклонил логин или пароль (только по тексту на странице входа,
// спека, раздел 16). Вход не повторяется до смены логина или пароля.
var ErrWrongPassword = errors.New("Rutracker: неверное имя пользователя или пароль — проверьте их в настройках")

// CaptchaError — Rutracker просит ввести код с картинки. Код не вводится (решение заказчика, спека
// этапа 7): сервер только предупреждает, картинка и поля остаются для журнала и исследований;
// до ввода кода автоматический вход не повторяется.
type CaptchaError struct {
	ImageURL  string // картинка static.rutracker.cc/captcha/…
	SID       string // скрытое поле cap_sid
	CodeField string // имя поля кода: cap_code_<хэш>
}

func (e *CaptchaError) Error() string {
	return "Rutracker: капча — вход не выполнен"
}

var (
	reForumHref   = regexp.MustCompile(`viewforum\.php\?f=(\d+)`)
	reTrackerHref = regexp.MustCompile(`tracker\.php\?f=(\d+)`)
	reIMDb        = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
)

func parseErr(block string) error { return &source.ParseError{Tracker: title, Block: block} }

// parseSearch — таблица результатов tracker.php: строки #tor-tbl tr.hl-tr (спека, раздел 6).
// Таблица без строк — «ничего не найдено», не ошибка.
func parseSearch(body []byte) ([]source.Release, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	table := doc.Find("#tor-tbl").First()
	if table.Length() == 0 {
		return nil, parseErr("таблица результатов (#tor-tbl)")
	}
	rows := table.Find("tr.hl-tr")
	out := make([]source.Release, 0, rows.Length())
	rows.Each(func(_ int, tr *goquery.Selection) {
		id, _ := tr.Attr("data-topic_id")
		name := htmltext.Clean(tr.Find("td.t-title-col a.tLink").First().Text())
		if id == "" || name == "" {
			return
		}
		r := source.Release{Tracker: Name, TopicID: id, Title: name}
		if href, ok := tr.Find("td.f-name-col a.f").Attr("href"); ok {
			if m := reTrackerHref.FindStringSubmatch(href); m != nil {
				r.CategoryID = m[1]
			}
		}
		if v, ok := tr.Find("td.tor-size").Attr("data-ts_text"); ok {
			r.Size, _ = strconv.ParseInt(v, 10, 64)
		}
		// Нет раздающих — вместо b.seedmed «2 дн.» и отрицательный data-ts_text: остаётся 0.
		r.Seeders = htmltext.FirstInt(tr.Find("b.seedmed").Text())
		r.Leechers = htmltext.FirstInt(tr.Find("td.leechmed").Text())
		// Последняя ячейка с data-ts_text — время добавления (unix).
		if v, ok := tr.Find("td[data-ts_text]").Last().Attr("data-ts_text"); ok {
			if ts, err := strconv.ParseInt(v, 10, 64); err == nil && ts > 0 {
				r.Added = time.Unix(ts, 0)
			}
		}
		out = append(out, r)
	})
	if rows.Length() > 0 && len(out) == 0 {
		return nil, parseErr("строки результатов (tr.hl-tr)")
	}
	return out, nil
}

// parseTopic — страница раздачи viewtopic.php (исходник до JS). TopicID заполняет источник.
// Гостю не видны размер и раздающие — они остаются нулями.
func parseTopic(body []byte) (source.Details, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return source.Details{}, err
	}
	d := source.Details{Release: source.Release{Tracker: Name}}
	if d.Title = htmltext.Clean(doc.Find("#topic-title").First().Text()); d.Title == "" {
		return d, parseErr("название раздачи (#topic-title)")
	}
	// Последняя ссылка на раздел в «хлебных крошках» — сам раздел.
	doc.Find(".t-breadcrumb-top a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if m := reForumHref.FindStringSubmatch(href); m != nil {
			d.CategoryID = m[1]
		}
	})
	post := doc.Find(".post_body").First()
	// Постер — первый var.postImgAligned; прочие var.postImg — скриншоты, баннеры, значки
	// (исследование, раздел 1). Только http(s): картинку потом качает сервер.
	if p, ok := post.Find("var.postImg.postImgAligned[title]").First().Attr("title"); ok &&
		(strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://")) {
		d.PosterURL = p
	}
	links := post.Find("a[href]")
	d.KinopoiskID = htmltext.FirstMatch(source.KinopoiskLink, links, "href")
	d.IMDbID = htmltext.FirstMatch(reIMDb, links, "href")
	// Ссылки /go/… — служебные ссылки и баннеры сайта («Набор в группу «Хранители»»), не текст раздачи.
	d.Description = htmltext.Text(post, `div.sp-wrap, script, style, var, a[href^="/go/"]`)
	href, _ := doc.Find("a.magnet-link[href]").First().Attr("href")
	if m := reBtih.FindStringSubmatch(href); m != nil {
		d.InfoHash = strings.ToLower(m[1])
	}
	if d.InfoHash == "" {
		return d, parseErr("magnet-ссылка (a.magnet-link)")
	}
	d.Magnet = buildMagnet(d.InfoHash)
	if v, ok := doc.Find("#tor-size-humn[title]").Attr("title"); ok {
		d.Size, _ = strconv.ParseInt(v, 10, 64)
	}
	d.Seeders = htmltext.FirstInt(doc.Find(".seed b").First().Text())
	d.Leechers = htmltext.FirstInt(doc.Find(".leech b").First().Text())
	return d, nil
}

// parseLogin — итог входа по странице ответа: вошли — в шапке a#logged-in-username; неверный
// пароль — только по тексту ошибки; капча — поле cap_sid (исследование, раздел 9).
func parseLogin(body []byte) error {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return err
	}
	if doc.Find("a#logged-in-username").Length() > 0 {
		return nil
	}
	if strings.Contains(strings.ToLower(doc.Find("#login-form-full h4.warnColor1").Text()), "неверн") {
		return ErrWrongPassword
	}
	if sid, ok := doc.Find(`input[name="cap_sid"]`).Attr("value"); ok {
		c := &CaptchaError{SID: sid}
		c.ImageURL, _ = doc.Find(`#login-form-full img[src*="/captcha/"]`).Attr("src")
		c.CodeField, _ = doc.Find(`input[name^="cap_code_"]`).Attr("name")
		return c
	}
	return parseErr("итог входа (a#logged-in-username или текст ошибки)")
}

// buildMagnet — magnet из infohash с трекерами Rutracker: только с DHT старт занимал 35 с, с ними
// — 6–13 с (исследование, раздел 2). HTTP-анонсы bt4/bt2 идут через прокси движка, UDP — напрямую.
func buildMagnet(infohash string) string {
	return "magnet:?xt=urn:btih:" + infohash +
		"&tr=" + url.QueryEscape("http://bt4.t-ru.org/ann?magnet") +
		"&tr=" + url.QueryEscape("http://bt2.t-ru.org/ann?magnet") +
		"&tr=" + url.QueryEscape("udp://opentor.net:6969")
}
