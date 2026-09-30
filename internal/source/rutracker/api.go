package rutracker

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/source"
)

// forumTree — дерево разделов api.rutracker.cc/v1/static/cat_forum_tree: категория → раздел →
// подразделы. Ключи категорий и разделов в JSON — строки, подразделы — числа.
type forumTree struct {
	Cats   map[string]string           // категория → название
	Forums map[string]string           // раздел или подраздел → название
	Tree   map[string]map[string][]int // категория → раздел → подразделы
}

func parseForumTree(b []byte) (*forumTree, error) {
	var v struct {
		Result struct {
			C    map[string]string           `json:"c"`
			F    map[string]string           `json:"f"`
			Tree map[string]map[string][]int `json:"tree"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Rutracker API: дерево разделов не читается: %w", err)
	}
	if len(v.Result.C) == 0 || len(v.Result.Tree) == 0 {
		return nil, &source.ParseError{Tracker: title, Block: "дерево разделов (cat_forum_tree)"}
	}
	return &forumTree{Cats: v.Result.C, Forums: v.Result.F, Tree: v.Result.Tree}, nil
}

// forumsUnder — все разделы и подразделы данных категорий (для поиска: спека, раздел 6).
func (t *forumTree) forumsUnder(cats ...string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cats {
		for f, subs := range t.Tree[c] {
			out[f] = true
			for _, s := range subs {
				out[strconv.Itoa(s)] = true
			}
		}
	}
	return out
}

func (t *forumTree) categories() []source.Category {
	var out []source.Category
	for _, c := range sortedIDs(t.Tree) {
		out = append(out, source.Category{ID: "c" + c, Name: t.Cats[c]})
		forums := t.Tree[c]
		for _, f := range sortedIDs(forums) {
			out = append(out, source.Category{ID: f, Name: t.Forums[f], ParentID: "c" + c})
			for _, s := range forums[f] {
				id := strconv.Itoa(s)
				out = append(out, source.Category{ID: id, Name: t.Forums[id], ParentID: f})
			}
		}
	}
	return out
}

// sortedIDs — ключи-номера по возрастанию номера.
func sortedIDs[V any](m map[string]V) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return cmp.Compare(x, y)
	})
	return keys
}

// treeRetryAfter — через сколько повторить неудачное обновление дерева разделов.
const treeRetryAfter = 10 * time.Minute

// treeRefreshTimeout — сколько ждать API, обновляя устаревшее дерево, когда старое есть: зависший
// API (не идёт через VPN, пакеты теряются) не должен съедать срок поиска. Тесты укорачивают.
var treeRefreshTimeout = 5 * time.Second

// forumTree — дерево из кэша; раз в сутки — заново. Не обновилось (API недоступен) — старое
// дерево: разделы меняются редко, а поиску оно нужно, пока форум работает (ревью этапа 4).
func (r *Rutracker) forumTree(ctx context.Context) (*forumTree, error) {
	now := r.now()
	r.mu.Lock()
	t, at, retryAt := r.tree, r.treeAt, r.treeRetryAt
	r.mu.Unlock()
	if t != nil && (now.Sub(at) < 24*time.Hour || now.Before(retryAt)) {
		return t, nil
	}
	refreshCtx := ctx
	if t != nil {
		var cancel context.CancelFunc
		refreshCtx, cancel = context.WithTimeout(ctx, treeRefreshTimeout)
		defer cancel()
	}
	b, err := r.apiBody(refreshCtx, "/v1/static/cat_forum_tree")
	var fresh *forumTree
	if err == nil {
		fresh, err = parseForumTree(b)
	}
	if err != nil {
		if t == nil || ctx.Err() != nil {
			return nil, err
		}
		r.log.Warn("Rutracker: дерево разделов не обновилось, работаю по старому", "err", err)
		r.mu.Lock()
		r.treeRetryAt = now.Add(treeRetryAfter)
		r.mu.Unlock()
		return t, nil
	}
	r.mu.Lock()
	r.tree, r.treeAt = fresh, now
	r.mu.Unlock()
	return fresh, nil
}

// Categories — дерево разделов для настроек: категории — «c<номер>», разделы и подразделы — номер.
func (r *Rutracker) Categories(ctx context.Context) ([]source.Category, error) {
	t, err := r.forumTree(ctx)
	if err != nil {
		return nil, err
	}
	return t.categories(), nil
}

// Top — первые limit раздач раздела по раздающим (limit ≤ 0 — весь список: каталог без ограничения по
// количеству, спека 11b, 7.2) по API: без входа и без Cloudflare, с infohash и цифрами, но без названий —
// их даёт Details или Recent. Только сам раздел; подфорумы собирает каталог.
func (r *Rutracker) Top(ctx context.Context, categoryID string, limit int) ([]source.Release, error) {
	if !isNumber(categoryID) {
		return nil, fmt.Errorf("Rutracker: раздел %q — не номер раздела", categoryID)
	}
	b, err := r.apiBody(ctx, "/v1/static/pvc/f/"+categoryID)
	if err != nil {
		return nil, err
	}
	rs, err := parsePVC(b, categoryID)
	if err != nil {
		return nil, err
	}
	sortBySeeders(rs)
	if limit > 0 && len(rs) > limit {
		rs = rs[:limit]
	}
	return rs, nil
}

// parsePVC — раздачи раздела: {"result":{"<тема>":[статус, раздающие, регистрация, размер,
// приоритет хранения, хранители, последний раздающий, infohash, автор, качающие]}}.
func parsePVC(b []byte, forumID string) ([]source.Release, error) {
	var v struct {
		Result map[string][]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Rutracker API: раздачи раздела %s не читаются: %w", forumID, err)
	}
	if v.Result == nil { // ответ с ошибкой вместо раздач
		return nil, &source.ParseError{Tracker: title, Block: "раздачи раздела (pvc)"}
	}
	out := make([]source.Release, 0, len(v.Result))
	for id, f := range v.Result {
		if len(f) < 10 {
			continue
		}
		var seeders, leechers int
		var reg, size int64
		var hash string
		if json.Unmarshal(f[1], &seeders) != nil || json.Unmarshal(f[2], &reg) != nil ||
			json.Unmarshal(f[3], &size) != nil || json.Unmarshal(f[7], &hash) != nil {
			continue
		}
		json.Unmarshal(f[9], &leechers)
		out = append(out, source.Release{Tracker: Name, TopicID: id, CategoryID: forumID, Seeders: seeders,
			Leechers: leechers, Size: size, Added: time.Unix(reg, 0), InfoHash: strings.ToLower(hash)})
	}
	return out, nil
}

// Recent — новые раздачи раздела из ленты Atom (feed.rutracker.cc): названия без пропуска
// Cloudflare. Запасной источник названий, когда Edge не справился (спека, раздел 6).
func (r *Rutracker) Recent(ctx context.Context, forumID string) ([]source.Release, error) {
	if !isNumber(forumID) {
		return nil, fmt.Errorf("Rutracker: раздел %q — не номер раздела", forumID)
	}
	p, err := r.api.Get(ctx, r.feed()+"/atom/f/"+forumID+".atom")
	if err != nil {
		return nil, err
	}
	if p.Status != http.StatusOK {
		return nil, fmt.Errorf("Rutracker: лента раздела %s — ответ %d", forumID, p.Status)
	}
	return parseAtom(p.Body)
}

var (
	reTopicID = regexp.MustCompile(`[?&]t=(\d+)`)
	reBtih    = regexp.MustCompile(`(?i)btih:([0-9a-f]{40})`)
)

func parseAtom(b []byte) ([]source.Release, error) {
	var feed struct {
		Entries []struct {
			Title string `xml:"title"`
			Links []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
			Updated  time.Time `xml:"updated"`
			Category struct {
				Term string `xml:"term,attr"`
			} `xml:"category"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(b, &feed); err != nil {
		return nil, fmt.Errorf("Rutracker: лента Atom не читается: %w", err)
	}
	out := make([]source.Release, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		r := source.Release{Tracker: Name, Title: strings.TrimSpace(e.Title), Added: e.Updated.UTC(),
			CategoryID: strings.TrimPrefix(e.Category.Term, "f-")}
		for _, l := range e.Links {
			if m := reTopicID.FindStringSubmatch(l.Href); m != nil && l.Rel == "" {
				r.TopicID = m[1]
			}
			if m := reBtih.FindStringSubmatch(l.Href); m != nil {
				r.InfoHash = strings.ToLower(m[1])
			}
		}
		if r.TopicID != "" && r.Title != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// apiBody — ответ API с кодом 200.
func (r *Rutracker) apiBody(ctx context.Context, path string) ([]byte, error) {
	p, err := r.api.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	if p.Status != http.StatusOK {
		return nil, fmt.Errorf("Rutracker API: %s — ответ %d", path, p.Status)
	}
	return p.Body, nil
}

func isNumber(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

func sortBySeeders(rs []source.Release) {
	slices.SortStableFunc(rs, func(a, b source.Release) int { return b.Seeders - a.Seeders })
}
