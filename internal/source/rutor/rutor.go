// Package rutor — источник раздач Rutor: топ категории, поиск, страница раздачи, .torrent.
// Обычный HTTP через прокси из настроек, без входа (спека, раздел 6).
package rutor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/htmltext"
)

// userAgent — обычный браузер: с ним снимались образцы страниц (spikes/misc/cmd/fetch).
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"

// videoCategories — разделы Rutor с видео, для настроек каталога.
var videoCategories = []source.Category{
	{ID: "1", Name: "Зарубежные фильмы"}, {ID: "5", Name: "Наши фильмы"},
	{ID: "12", Name: "Научно-популярные фильмы"}, {ID: "4", Name: "Зарубежные сериалы"},
	{ID: "16", Name: "Наши сериалы"}, {ID: "6", Name: "Телевизор"},
	{ID: "7", Name: "Мультипликация"}, {ID: "10", Name: "Аниме"}, {ID: "15", Name: "Юмор"},
	{ID: "13", Name: "Спорт и здоровье"}, {ID: "17", Name: "Иностранные релизы"},
}

// searchCategories — где ищет поиск, по категории за запрос: во всех видеокатегориях (жалоба 2026-10-02:
// «Телевизор» с шоу и телепередачами выпадал — из 9 раздач «Фермы Кларксона» находилась одна).
var searchCategories = func() []string {
	out := make([]string, len(videoCategories))
	for i, c := range videoCategories {
		out[i] = c.ID
	}
	return out
}()

// Адресов Rutor в программе нет: адрес сайта или зеркала вводит пользователь, адрес .torrent —
// по правилу из него (спека этапа 11a, раздел 6). Без адреса источник выключен.
type Options struct {
	Proxy        *netx.Proxy   // прокси для трекеров из настроек; nil — напрямую
	Mirrors      []string      // адреса сайта; пусто — адрес не введён, источник выключен
	DownloadBase string        // адрес .torrent; пусто — по правилу из первого адреса сайта
	Rate         rate.Limit    // 0 — 1 запрос/с (тесты ускоряют)
	Timeout      time.Duration // 0 — 90 с
	Log          *slog.Logger  // nil — без журнала
}

type Rutor struct {
	c        *netx.Client
	searches chan struct{} // не больше трёх запросов поиска одновременно на весь Rutor (спека, раздел 7)

	mu       sync.Mutex
	download string // адрес .torrent без «/» на конце; меняет SetAddresses
}

var _ source.Source = (*Rutor)(nil)

func New(o Options) (*Rutor, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	c, err := netx.NewClient(netx.Options{
		Name: title, Proxy: o.Proxy, UserAgent: userAgent, Classify: classify, ChallengeIsMirrorDown: true,
		Rate: o.Rate, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	r := &Rutor{c: c, searches: make(chan struct{}, 3)}
	if err := r.setAddresses(o.Mirrors, o.DownloadBase); err != nil {
		return nil, err
	}
	return r, nil
}

// SetAddresses — адрес сайта (или зеркала) и адрес .torrent из настроек; download "" — по
// правилу из адреса сайта, site "" — источник выключен. Действует со следующего запроса.
func (r *Rutor) SetAddresses(site, download string) error {
	var mirrors []string
	if site != "" {
		mirrors = []string{site}
	}
	return r.setAddresses(mirrors, download)
}

func (r *Rutor) setAddresses(mirrors []string, download string) error {
	mirrors = slices.Clone(mirrors)
	if len(mirrors) == 0 {
		download = ""
	} else if download == "" {
		download = source.RutorDownload(strings.TrimRight(mirrors[0], "/"))
	}
	var extra []string
	if download != "" {
		dl, err := url.Parse(download)
		if err != nil || dl.Host == "" {
			return fmt.Errorf("Rutor: адрес .torrent %q — не адрес сайта", download)
		}
		extra = []string{dl.Host}
	}
	if err := r.c.SetMirrors(mirrors, extra...); err != nil {
		return err
	}
	r.mu.Lock()
	r.download = strings.TrimRight(download, "/")
	r.mu.Unlock()
	return nil
}

// Configured — адрес Rutor введён.
func (r *Rutor) Configured() bool { return r.c.Configured() }

// Check — «Проверить» в мастере начальных настроек: список раздела открывается и похож на Rutor.
// Ошибки — как у запросов: ErrNotConfigured, netx.ErrProxyDown, netx.ErrNotTracker, netx.ErrTrackerDown.
func (r *Rutor) Check(ctx context.Context) error {
	_, err := r.page(ctx, "/browse/0/1/0/2")
	return err
}

func (r *Rutor) Name() string { return Name }

// Mirror — зеркало, ответившее последним (каталог запомнит его между запусками — этап 5).
func (r *Rutor) Mirror() string { return r.c.Mirror() }

// TopicURL — страница раздачи на текущем зеркале (ссылка «На трекере» в пульте); "" — адрес не введён.
func (r *Rutor) TopicURL(id string) string {
	m := r.c.Mirror()
	if m == "" {
		return ""
	}
	return m + "/torrent/" + id
}

// Categories — видеоразделы Rutor; список постоянный, на трекер за ним не ходим.
func (r *Rutor) Categories(context.Context) ([]source.Category, error) {
	return slices.Clone(videoCategories), nil
}

// Top — первые limit раздач категории по раздающим (limit ≤ 0 — все 100 строк страницы).
// Цифры в списке Rutor неточны, поэтому сортируем сами (спека, раздел 6).
func (r *Rutor) Top(ctx context.Context, categoryID string, limit int) ([]source.Release, error) {
	rs, _, err := r.TopPage(ctx, categoryID, 0)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(rs) > limit {
		rs = rs[:limit]
	}
	return rs, nil
}

// TopPage — страница категории по раздающим (0 — первая, по 100 строк): порции каталога глубже первой
// сотни (спека 11b, 7.2). more — страница полная, дальше, возможно, есть ещё.
func (r *Rutor) TopPage(ctx context.Context, categoryID string, page int) (rs []source.Release, more bool, err error) {
	if rs, more, err = r.browse(ctx, categoryID, page, "2"); err != nil {
		return nil, false, err
	}
	sortBySeeders(rs)
	return rs, more, nil
}

// sortCode — порядок раздела → последняя часть адреса /browse (проверено вживую 2026-10-01: 0 — новые
// первыми, 4 — по качающим, 2 — по раздающим).
var sortCode = map[string]string{source.OrderNew: "0", source.OrderLeechers: "4", source.OrderSeeders: "2"}

// SortOrders — порядки раздела, которые каталог берёт у сайта (план 14Б); числа скачиваний у Rutor нет.
func (r *Rutor) SortOrders() []string { return []string{source.OrderLeechers, source.OrderNew} }

// SortedPage — страница раздела (forums — один его номер) в порядке сайта order, по 100 строк, page 0 —
// первая; строки не пересортированы. more — страница полная.
func (r *Rutor) SortedPage(ctx context.Context, forums []string, order string, page int) ([]source.Release, bool, error) {
	code, ok := sortCode[order]
	if !ok {
		return nil, false, fmt.Errorf("Rutor: порядка %q нет", order)
	}
	if len(forums) != 1 {
		return nil, false, fmt.Errorf("Rutor: раздел — один номер, а не %q", forums)
	}
	return r.browse(ctx, forums[0], page, code)
}

// browse — страница раздела /browse/<стр>/<раздел>/0/<порядок> по 100 строк.
func (r *Rutor) browse(ctx context.Context, categoryID string, page int, code string) (rs []source.Release, more bool, err error) {
	if !isNumber(categoryID) || page < 0 {
		return nil, false, fmt.Errorf("Rutor: категория %q — не номер", categoryID)
	}
	p, err := r.page(ctx, "/browse/"+strconv.Itoa(page)+"/"+categoryID+"/0/"+code)
	if err != nil {
		return nil, false, err
	}
	rs, err = parseList(p.Body)
	if err != nil {
		return nil, false, err
	}
	for i := range rs {
		rs[i].CategoryID = categoryID
	}
	return rs, len(rs) >= 100, nil
}

// Search ищет по видеокатегориям: запрос на категорию, по порядку важности, не больше трёх одновременно и мимо
// ограничителя «1 в секунду» (спека, раздел 7). Часть категорий не ответила или вышло время —
// возвращаем найденное вместе с *source.PartialError; не ответила ни одна — только ошибку.
func (r *Rutor) Search(ctx context.Context, query string) ([]source.Release, error) {
	if !r.Configured() {
		return nil, fmt.Errorf("%s: %w", title, source.ErrNotConfigured)
	}
	q := searchQuery(query)
	if q == "" {
		return nil, errors.New("Rutor: пустой поисковый запрос")
	}
	found := make([][]source.Release, len(searchCategories))
	errs := make([]error, len(searchCategories))
	var wg sync.WaitGroup
	// Места занимаются по порядку важности категорий: при медленном Rutor срок отрезает хвост списка, а не
	// случайные категории (ревью 15Д).
	for i, cat := range searchCategories {
		select {
		case r.searches <- struct{}{}:
		case <-ctx.Done():
			errs[i] = ctx.Err()
			continue
		}
		wg.Go(func() {
			defer func() { <-r.searches }()
			found[i], errs[i] = r.searchIn(ctx, cat, q)
		})
	}
	wg.Wait()
	// Сводим в порядке категорий, а не ответов: результат не зависит от того, кто успел первым.
	var out []source.Release
	seen := map[string]bool{}
	var failed []error
	for i := range searchCategories {
		if errs[i] != nil {
			failed = append(failed, errs[i])
			continue
		}
		for _, rel := range found[i] {
			if !seen[rel.TopicID] {
				seen[rel.TopicID] = true
				out = append(out, rel)
			}
		}
	}
	if len(failed) == len(searchCategories) {
		return nil, failed[0]
	}
	sortBySeeders(out)
	if len(failed) > 0 {
		cause := failed[0]
		if ctx.Err() != nil {
			cause = ctx.Err() // вышло время на весь поиск — это главное, что нужно знать каталогу
		}
		return out, &source.PartialError{Tracker: title, Failed: len(failed), Total: len(searchCategories), Err: cause}
	}
	return out, nil
}

func (r *Rutor) searchIn(ctx context.Context, cat, q string) ([]source.Release, error) {
	p, err := r.page(ctx, "/search/0/"+cat+"/100/2/"+url.PathEscape(q), netx.WithoutLimit())
	if err != nil {
		return nil, err
	}
	rs, err := parseList(p.Body)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].CategoryID = cat
	}
	return rs, nil
}

// searchQuery — запрос без лишних пробелов и без «/» и «\»: закодированную косую черту
// nginx раскодирует в разделитель пути, и адрес поиска сломается. Длинный запрос
// обрезается до 100 символов.
func searchQuery(q string) string {
	q = strings.NewReplacer("/", " ", `\`, " ").Replace(q)
	q = htmltext.Clean(q)
	if strings.Trim(q, ".") == "" {
		return "" // «.» и «..» в пути — не запрос, а переход по папкам
	}
	if rs := []rune(q); len(rs) > 100 {
		q = strings.TrimSpace(string(rs[:100]))
	}
	return q
}

// DetailsAtOnce — сколько страниц раздач каталог качает сразу (спека 11b, 14.4): Rutor отдаёт страницу
// 4–77 с (вживую 2026-10-01), по одной — 2–7 раздач в минуту; новый запрос — всё так же не чаще ограничителя.
func (r *Rutor) DetailsAtOnce() int { return 4 }

// Details — страница раздачи: название, описание, постер, id Кинопоиска, magnet, цифры.
func (r *Rutor) Details(ctx context.Context, topicID string) (source.Details, error) {
	if !isNumber(topicID) {
		return source.Details{}, fmt.Errorf("Rutor: номер раздачи %q — не число", topicID)
	}
	p, err := r.page(ctx, "/torrent/"+topicID)
	if err != nil {
		return source.Details{}, err
	}
	d, err := parseTopic(p.Body, p.URL)
	if err != nil {
		return source.Details{}, err
	}
	d.TopicID = topicID
	d.TorrentURL = r.torrentURL(topicID)
	return d, nil
}

// Torrent скачивает .torrent раздачи. Rutor отдаёт его без входа; каталог качает его
// заранее, чтобы список файлов был сразу (спека, раздел 7).
func (r *Rutor) Torrent(ctx context.Context, topicID string) ([]byte, error) {
	if !isNumber(topicID) {
		return nil, fmt.Errorf("Rutor: номер раздачи %q — не число", topicID)
	}
	p, err := r.page(ctx, r.torrentURL(topicID))
	if err != nil {
		return nil, err
	}
	mi, err := metainfo.Load(bytes.NewReader(p.Body))
	if err == nil {
		_, err = mi.UnmarshalInfo()
	}
	if err != nil {
		return nil, fmt.Errorf("Rutor: вместо .torrent раздачи %s пришло что-то другое: %w", topicID, err)
	}
	return p.Body, nil
}

func (r *Rutor) torrentURL(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.download + "/download/" + id
}

// page — страница с кодом 200; другие коды — ошибка без смены зеркала (5xx и 451 зеркало
// уже сменили в netx).
func (r *Rutor) page(ctx context.Context, path string, opts ...netx.GetOption) (*netx.Page, error) {
	p, err := r.c.Get(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	if p.Status != http.StatusOK {
		return nil, fmt.Errorf("Rutor: %s — ответ %d", p.URL.Path, p.Status)
	}
	return p, nil
}

// classify — признаки Rutor поверх общих (спека, раздел 5): редирект на /d.php — раздача
// удалена; страница 200 без каркаса Rutor — заглушка или чужой сайт вместо зеркала.
func classify(p *netx.Page) netx.Verdict {
	if p.URL.Path == "/d.php" {
		return netx.Removed
	}
	if p.Status == http.StatusOK && strings.Contains(p.Header.Get("Content-Type"), "text/html") && !isRutorPage(p.Body) {
		return netx.MirrorDown
	}
	return netx.OK
}

// reRutorTitle — заголовок Rutor: у всех зеркал «rutor.info :: …», пробелы после <title> — не повод.
var reRutorTitle = regexp.MustCompile(`(?i)<title>\s*rutor`)

// isRutorPage — каркас сайта: div#logo, div#menu и «rutor» в заголовке; похожий шаблон без него —
// чужой сайт (ревью этапа 3). Заголовок — в начале страницы: вся страница не копируется.
func isRutorPage(b []byte) bool {
	return bytes.Contains(b, []byte(`id="logo"`)) && bytes.Contains(b, []byte(`id="menu"`)) &&
		reRutorTitle.Match(b[:min(len(b), 4096)])
}

func sortBySeeders(rs []source.Release) {
	slices.SortStableFunc(rs, func(a, b source.Release) int { return b.Seeders - a.Seeders })
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
