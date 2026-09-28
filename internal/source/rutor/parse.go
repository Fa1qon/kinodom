package rutor

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"kinodom/internal/source"
	"kinodom/internal/source/htmltext"
)

// Name — имя источника в каталоге; title — в текстах ошибок.
const (
	Name  = "rutor"
	title = "Rutor"
)

// msk — время на страницах Rutor московское (летнего времени в Москве нет с 2014 года).
var msk = time.FixedZone("MSK", 3*60*60)

var (
	reTopicHref = regexp.MustCompile(`^/torrent/(\d+)(?:/|$)`)
	reBtih      = regexp.MustCompile(`(?i)btih:([0-9a-f]{40})`)
	months      = map[string]time.Month{"Янв": 1, "Фев": 2, "Мар": 3, "Апр": 4, "Май": 5, "Июн": 6, "Июл": 7, "Авг": 8, "Сен": 9, "Окт": 10, "Ноя": 11, "Дек": 12}
	sizeUnits   = map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}
)

func parseErr(block string) error { return &source.ParseError{Tracker: title, Block: block} }

// parseList разбирает таблицы раздач (div#index) страниц /browse и /search: строки tr.gai
// и tr.tum. Таблица с заголовком (tr.backgr) без строк — «ничего не найдено», не ошибка.
func parseList(body []byte) ([]source.Release, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	tables := doc.Find("div#index table").Has("tr.backgr")
	if tables.Length() == 0 {
		return nil, parseErr("таблица раздач")
	}
	rows := tables.Find("tr.gai, tr.tum")
	out := make([]source.Release, 0, rows.Length())
	rows.Each(func(_ int, tr *goquery.Selection) {
		if r, ok := parseRow(tr); ok {
			out = append(out, r)
		}
	})
	if rows.Length() > 0 && len(out) == 0 {
		return nil, parseErr("строки таблицы раздач")
	}
	return out, nil
}

// parseRow — одна строка: дата | ссылки (.torrent, magnet, раздача) | [комментарии] |
// размер | пиры. Колонка комментариев есть не у всех строк — размер и пиры берём с конца.
func parseRow(tr *goquery.Selection) (source.Release, bool) {
	tds := tr.ChildrenFiltered("td")
	if tds.Length() < 4 {
		return source.Release{}, false
	}
	r := source.Release{Tracker: Name, Added: parseListDate(htmltext.Clean(tds.Eq(0).Text()))}
	tds.Eq(1).Find("a").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if m := reTopicHref.FindStringSubmatch(href); m != nil {
			r.TopicID, r.Title = m[1], htmltext.Clean(a.Text())
		} else if strings.HasPrefix(href, "magnet:") {
			r.InfoHash = infoHash(href)
		}
	})
	r.Size = parseSize(htmltext.Clean(tds.Eq(tds.Length() - 2).Text()))
	peers := tds.Last()
	r.Seeders = htmltext.FirstInt(peers.Find("span.green").Text())
	r.Leechers = htmltext.FirstInt(peers.Find("span.red").Text())
	return r, r.TopicID != "" && r.Title != ""
}

func infoHash(magnet string) string {
	if m := reBtih.FindStringSubmatch(magnet); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// parseListDate — «15 Мар 26» → 15.03.2026 по Москве; непонятная дата — пустая.
func parseListDate(s string) time.Time {
	f := strings.Fields(s)
	if len(f) != 3 {
		return time.Time{}
	}
	d, err1 := strconv.Atoi(f[0])
	y, err2 := strconv.Atoi(f[2])
	m, ok := months[f[1]]
	if err1 != nil || err2 != nil || !ok {
		return time.Time{}
	}
	return time.Date(2000+y, m, d, 0, 0, 0, 0, msk)
}

// parseSize — «3.87 GB» → байты. Rutor считает в двоичных единицах: у раздачи 1077013
// «3.87 GB (4153309470 Bytes)».
func parseSize(s string) int64 {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return int64(v * sizeUnits[strings.ToUpper(f[1])])
}

// categoryBySlug — на странице раздачи категория указана слагом (/nauchno_popularnoe).
var categoryBySlug = map[string]string{
	"kino": "1", "nashe_kino": "5", "nauchno_popularnoe": "12", "seriali": "4",
	"nashi_seriali": "16", "tv": "6", "multiki": "7", "anime": "10", "jumor": "15",
	"inostrannoe": "17", "audio": "2", "games": "8", "soft": "9", "sport": "13",
	"byt": "14", "knigi": "11", "other": "3",
}

var (
	reBytes         = regexp.MustCompile(`\((\d+) Bytes\)`)
	reKinopoiskLink = regexp.MustCompile(`kinopoisk\.ru/(?:film|series)/(\d+)`)
	reKinopoiskImg  = regexp.MustCompile(`kinopoisk\.ru/(?:rating/)?(\d+)\.gif`)
	reIMDb          = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
)

// parseTopic разбирает страницу раздачи /torrent/{id}. TopicID и TorrentURL заполняет источник.
func parseTopic(body []byte, pageURL *url.URL) (source.Details, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return source.Details{}, err
	}
	d := source.Details{Release: source.Release{Tracker: Name}}
	table := doc.Find("table#details").First()
	if table.Length() == 0 {
		return d, parseErr("описание раздачи (table#details)")
	}
	if d.Title = htmltext.Clean(doc.Find("h1").First().Text()); d.Title == "" {
		return d, parseErr("название раздачи (h1)")
	}
	doc.Find("div#download a").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		href, _ := a.Attr("href")
		if strings.HasPrefix(href, "magnet:") {
			d.Magnet, d.InfoHash = href, infoHash(href)
			return false
		}
		return true
	})
	if d.InfoHash == "" {
		return d, parseErr("magnet-ссылка (div#download)")
	}
	// Строки самой таблицы, без таблиц внутри описания (tbody парсер HTML вставляет сам).
	rows := table.ChildrenFiltered("tbody").ChildrenFiltered("tr")
	desc := rows.First().ChildrenFiltered("td").Eq(1)
	d.PosterURL = poster(desc, pageURL)
	d.KinopoiskID = kinopoiskID(desc)
	d.IMDbID = htmltext.FirstMatch(reIMDb, desc.Find("a[href]"), "href")
	d.Description = htmltext.Text(desc, "div.hidewrap, script, style, textarea")
	rows.Each(func(_ int, tr *goquery.Selection) {
		h := tr.ChildrenFiltered("td.header")
		if h.Length() == 0 {
			return
		}
		v := h.Next()
		switch htmltext.Clean(h.Text()) {
		case "Категория":
			href, _ := v.Find("a").Attr("href")
			d.CategoryID = categoryBySlug[strings.Trim(href, "/")]
		case "Раздают":
			d.Seeders = htmltext.FirstInt(v.Text())
		case "Качают":
			d.Leechers = htmltext.FirstInt(v.Text())
		case "Добавлен":
			d.Added = parseTopicDate(v.Text())
		case "Размер":
			if m := reBytes.FindStringSubmatch(v.Text()); m != nil {
				d.Size, _ = strconv.ParseInt(m[1], 10, 64)
			}
		}
	})
	return d, nil
}

// poster — первая картинка описания, кроме картинок-рейтингов (s.rutor.info/imdb/pic/…,
// rating.kinopoisk.ru/…, kinopoisk.ru/rating/…). Так постер находится в 9 раздачах из 9.
func poster(desc *goquery.Selection, pageURL *url.URL) string {
	var out string
	desc.Find("img[src]").EachWithBreak(func(_ int, img *goquery.Selection) bool {
		src, _ := img.Attr("src")
		low := strings.ToLower(src)
		if strings.Contains(low, "rutor.info/imdb/") || strings.Contains(low, "kinopoisk.ru") {
			return true
		}
		if u, err := pageURL.Parse(src); err == nil {
			out = u.String()
		}
		return false
	})
	return out
}

// kinopoiskID — номер фильма на Кинопоиске: из ссылки на фильм или сериал, иначе из картинки
// рейтинга. С ним каталогу не нужен поиск по названию — это экономит квоту (спека, раздел 8).
func kinopoiskID(desc *goquery.Selection) string {
	if id := htmltext.FirstMatch(reKinopoiskLink, desc.Find("a[href]"), "href"); id != "" {
		return id
	}
	return htmltext.FirstMatch(reKinopoiskImg, desc.Find("img[src]"), "src")
}

// parseTopicDate — «15-03-2026 0:52:32 (7 месяцев назад)» → время по Москве.
func parseTopicDate(s string) time.Time {
	s, _, _ = strings.Cut(htmltext.Clean(s), " (")
	t, err := time.ParseInLocation("02-01-2006 15:04:05", s, msk)
	if err != nil {
		return time.Time{}
	}
	return t
}
