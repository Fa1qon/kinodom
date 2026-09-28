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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/htmltext"
)

// Встроенные зеркала и адрес .torrent (спека, раздел 5).
var (
	DefaultMirrors      = []string{"https://rutor.info", "https://rutor.is"}
	DefaultDownloadBase = "https://d.rutor.info"
)

// userAgent — обычный браузер: с ним снимались образцы страниц (spikes/misc/cmd/fetch).
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"

// searchCategories — где ищет поиск (спека, раздел 6). У Rutor одна категория за запрос.
var searchCategories = []string{"1", "5", "12", "4", "16", "7"}

// videoCategories — разделы Rutor с видео, для настроек каталога.
var videoCategories = []source.Category{
	{ID: "1", Name: "Зарубежные фильмы"}, {ID: "5", Name: "Наши фильмы"},
	{ID: "12", Name: "Научно-популярные фильмы"}, {ID: "4", Name: "Зарубежные сериалы"},
	{ID: "16", Name: "Наши сериалы"}, {ID: "6", Name: "Телевизор"},
	{ID: "7", Name: "Мультипликация"}, {ID: "10", Name: "Аниме"}, {ID: "15", Name: "Юмор"},
}

type Options struct {
	Proxy        string        // прокси для трекеров из настроек; пусто — напрямую
	Mirrors      []string      // пусто — DefaultMirrors
	DownloadBase string        // пусто — DefaultDownloadBase
	Rate         rate.Limit    // 0 — 1 запрос/с (тесты ускоряют)
	Timeout      time.Duration // 0 — 90 с
	Log          *slog.Logger  // nil — без журнала
}

type Rutor struct {
	c        *netx.Client
	download string
	searches chan struct{} // не больше трёх запросов поиска одновременно на весь Rutor (спека, раздел 7)
}

var _ source.Source = (*Rutor)(nil)

func New(o Options) (*Rutor, error) {
	// Копия: список по умолчанию — общий, его правка не должна менять работающий источник.
	if len(o.Mirrors) == 0 {
		o.Mirrors = DefaultMirrors
	}
	o.Mirrors = slices.Clone(o.Mirrors)
	if o.DownloadBase == "" {
		o.DownloadBase = DefaultDownloadBase
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	dl, err := url.Parse(o.DownloadBase)
	if err != nil || dl.Host == "" {
		return nil, fmt.Errorf("Rutor: адрес .torrent %q — не адрес сайта", o.DownloadBase)
	}
	c, err := netx.NewClient(netx.Options{
		Name: title, Mirrors: o.Mirrors, ExtraHosts: []string{dl.Host},
		Proxy: o.Proxy, UserAgent: userAgent, Classify: classify, ChallengeIsMirrorDown: true,
		Rate: o.Rate, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	return &Rutor{c: c, download: strings.TrimRight(o.DownloadBase, "/"), searches: make(chan struct{}, 3)}, nil
}

func (r *Rutor) Name() string { return Name }

// Mirror — зеркало, ответившее последним (каталог запомнит его между запусками — этап 5).
func (r *Rutor) Mirror() string { return r.c.Mirror() }

// Categories — видеоразделы Rutor; список постоянный, на трекер за ним не ходим.
func (r *Rutor) Categories(context.Context) ([]source.Category, error) {
	return slices.Clone(videoCategories), nil
}

// Top — первые limit раздач категории по раздающим (limit ≤ 0 — все 100 строк страницы).
// Цифры в списке Rutor неточны, поэтому сортируем сами (спека, раздел 6).
func (r *Rutor) Top(ctx context.Context, categoryID string, limit int) ([]source.Release, error) {
	if !isNumber(categoryID) {
		return nil, fmt.Errorf("Rutor: категория %q — не номер", categoryID)
	}
	p, err := r.page(ctx, "/browse/0/"+categoryID+"/0/2")
	if err != nil {
		return nil, err
	}
	rs, err := parseList(p.Body)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].CategoryID = categoryID
	}
	sortBySeeders(rs)
	if limit > 0 && len(rs) > limit {
		rs = rs[:limit]
	}
	return rs, nil
}

// Search ищет по видеокатегориям: шесть запросов, не больше трёх одновременно и мимо
// ограничителя «1 в секунду» (спека, раздел 7). Часть категорий не ответила или вышло время —
// возвращаем найденное вместе с *source.PartialError; не ответила ни одна — только ошибку.
func (r *Rutor) Search(ctx context.Context, query string) ([]source.Release, error) {
	q := searchQuery(query)
	if q == "" {
		return nil, errors.New("Rutor: пустой поисковый запрос")
	}
	found := make([][]source.Release, len(searchCategories))
	errs := make([]error, len(searchCategories))
	var wg sync.WaitGroup
	for i, cat := range searchCategories {
		wg.Go(func() {
			select {
			case r.searches <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
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

func (r *Rutor) torrentURL(id string) string { return r.download + "/download/" + id }

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

// isRutorPage — каркас сайта: div#logo, div#menu и «rutor» в заголовке. У всех зеркал он
// «rutor.info :: …»; похожий шаблон без него — чужой сайт (ревью этапа 3).
func isRutorPage(b []byte) bool {
	return bytes.Contains(b, []byte(`id="logo"`)) && bytes.Contains(b, []byte(`id="menu"`)) &&
		bytes.Contains(bytes.ToLower(b), []byte("<title>rutor"))
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
