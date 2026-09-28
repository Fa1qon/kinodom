package rutor

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"kinodom/internal/source"
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
	reInt       = regexp.MustCompile(`\d+`)
	months      = map[string]time.Month{"Янв": 1, "Фев": 2, "Мар": 3, "Апр": 4, "Май": 5, "Июн": 6, "Июл": 7, "Авг": 8, "Сен": 9, "Окт": 10, "Ноя": 11, "Дек": 12}
	sizeUnits   = map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}
)

func parseErr(block string) error { return &source.ErrParse{Tracker: title, Block: block} }

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
	r := source.Release{Tracker: Name, Added: parseListDate(clean(tds.Eq(0).Text()))}
	tds.Eq(1).Find("a").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if m := reTopicHref.FindStringSubmatch(href); m != nil {
			r.TopicID, r.Title = m[1], clean(a.Text())
		} else if strings.HasPrefix(href, "magnet:") {
			r.InfoHash = infoHash(href)
		}
	})
	r.Size = parseSize(clean(tds.Eq(tds.Length() - 2).Text()))
	peers := tds.Last()
	r.Seeders = firstInt(peers.Find("span.green").Text())
	r.Leechers = firstInt(peers.Find("span.red").Text())
	return r, r.TopicID != "" && r.Title != ""
}

// clean — текст с одиночными пробелами (strings.Fields режет и по неразрывному пробелу).
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func infoHash(magnet string) string {
	if m := reBtih.FindStringSubmatch(magnet); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func firstInt(s string) int {
	n, _ := strconv.Atoi(reInt.FindString(s))
	return n
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
