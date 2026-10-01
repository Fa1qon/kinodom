package catalog

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"kinodom/internal/httpx"
	"kinodom/internal/meta"
)

// PageSize — раздач на странице каталога: шесть рядов по четыре на широком экране, по два — на
// узком.
const PageSize = 24

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

// Register — маршруты каталога (спека этапа 7, раздел 5.8).
func (c *Catalog) Register(r Router) {
	r.Handle("GET /api/v1/sources/{tracker}/categories", c.Name(), http.HandlerFunc(c.handleTree))
	r.Handle("GET /api/v1/catalog/sections", c.Name(), http.HandlerFunc(c.handleSections))
	r.Handle("GET /api/v1/catalog", c.Name(), http.HandlerFunc(c.handleList))
	r.Handle("GET /api/v1/releases/{id}/variants", c.Name(), http.HandlerFunc(c.handleVariants))
	r.Handle("GET /api/v1/search", c.Name(), http.HandlerFunc(c.handleSearch))
	r.Handle("GET /api/v1/search/history", c.Name(), http.HandlerFunc(c.handleHistory))
	r.Handle("DELETE /api/v1/search/history", c.Name(), http.HandlerFunc(c.handleForget))
	r.Handle("POST /api/v1/catalog/refresh", c.Name(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Refresh()
		httpx.WriteJSON(w, http.StatusAccepted, struct{}{})
	}))
}

// EntryView — раздача в списке каталога и поиска для пульта и телевизора.
type EntryView struct {
	ID        int64   `json:"id"`
	Tracker   string  `json:"tracker"`
	Title     string  `json:"title"` // заголовок раздачи целиком; "" — ещё не загружен
	Name      string  `json:"name"`  // первое (обычно русское) название из заголовка
	Original  string  `json:"original"`
	Year      int     `json:"year"` // 0 — не найден
	Quality   string  `json:"quality"`
	Category  string  `json:"category"`
	Seeders   int     `json:"seeders"`
	Leechers  int     `json:"leechers"`
	Size      int64   `json:"size"`
	ImageKey  string  `json:"imageKey"`           // картинка — /img/{imageKey}; "" — нет
	Kinopoisk float64 `json:"kinopoisk"`          // рейтинг Кинопоиска; 0 — нет
	Format    string  `json:"format"`             // «MKV», «AVI, MKV»; "" — неизвестен
	Season    string  `json:"season"`             // сезон и серии из заголовка: «S01»; "" — нет
	Variants  int     `json:"variants,omitempty"` // раздач фильма на обоих трекерах — у карточки каталога
	// DetailsPending — страницу раздачи ещё не загружали (найдено поиском): формат и номер Кинопоиска
	// появятся после догрузки.
	DetailsPending bool `json:"detailsPending"`
}

// View — раздача для API.
func (e Entry) View() EntryView {
	t := meta.ParseTitle(e.Title)
	return EntryView{ID: e.ID, Tracker: e.Tracker, Title: e.Title, Name: t.Ru, Original: t.Orig, Year: t.Year,
		Quality: e.Quality, Category: e.Category, Seeders: e.Seeders, Leechers: e.Leechers, Size: e.Size,
		ImageKey: e.ImageKey, Kinopoisk: e.Rating.Kinopoisk, Format: e.Format, Season: t.Season, Variants: e.Variants,
		DetailsPending: e.DetailsPending}
}

// ListView — порция каталога трекера: карточки раздела после места after.
type ListView struct {
	Section   string      `json:"section"`   // раздел, чья это порция
	Next      int         `json:"next"`      // место последней карточки: следующая порция — after=next
	More      bool        `json:"more"`      // есть ещё (у трекера или в базе); общее число раздач пульт не показывает
	UpdatedAt *time.Time  `json:"updatedAt"` // последнее удачное обновление разделов трекера; null — ещё не было
	Entries   []EntryView `json:"entries"`
}

// portionsPerList — сколько порций с трекера может взять одна просьба пульта: склейка дублей бывает
// плотной, но выкачивать трекер одной просьбой нельзя.
const portionsPerList = 3

// handleList — порция раздела трекера по месту (спека этапа 7, раздел 5.4; 11b, 7.2): карточки после
// места after (-1 — с начала). Раздела в запросе нет — первый раздел трекера, где есть раздачи. Карточек
// дальше меньше двух порций — с трекера следующая (не больше portionsPerList за просьбу).
func (c *Catalog) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("tracker")
	if !c.tracker(w, name) {
		return
	}
	after, err := strconv.Atoi(q.Get("after"))
	if err != nil || after < -1 {
		after = -1
	}
	out := ListView{Section: q.Get("section"), Next: after, Entries: []EntryView{}}
	if out.Section == "" {
		secs, err := c.trackerSections(r.Context(), name)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "разделы не читаются: "+err.Error())
			return
		}
		if len(secs) > 0 {
			out.Section = secs[0].ID
		}
	}
	if out.Section != "" {
		cat := CategoryRef{name, out.Section}
		trackerMore := slices.Contains(c.enabled(), cat) && !c.deepEnded(cat)
		var (
			es   []Entry
			next int
			rest int
			perr error
		)
		for portions := 0; ; portions++ {
			if es, next, rest, err = c.SectionPage(r.Context(), name, out.Section, after, PageSize); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "каталог не читается: "+err.Error())
				return
			}
			// Запас — ещё порция пульта в базе: следующая просьба не ждёт трекер.
			if !trackerMore || portions == portionsPerList || (len(es) == PageSize && rest >= PageSize) {
				break
			}
			more, err := c.ensureOne(r.Context(), cat)
			if isDBError(err) {
				httpx.WriteError(w, http.StatusInternalServerError, "каталог не читается: "+err.Error())
				return
			}
			if err != nil {
				perr = err
				break
			}
			trackerMore = more
		}
		if perr != nil && len(es) == 0 {
			// Порция не пришла и показать нечего: ошибка — пульт повторит по тому же месту (11b-А).
			c.log.Info("каталог: порция раздела не пришла", "tracker", name, "section", out.Section, "err", perr)
			httpx.WriteError(w, http.StatusBadGateway, "Порция каталога не пришла: "+perr.Error())
			return
		}
		out.Next, out.More = next, rest > 0 || trackerMore
		ids := make([]int64, 0, len(es))
		for _, e := range es {
			out.Entries = append(out.Entries, e.View())
			ids = append(ids, e.ID)
		}
		c.enqueueFound(r.Context(), name, ids) // без страницы раздачи — в догрузку вне очереди по порядку показа
	}
	at, err := c.UpdatedAt(r.Context(), name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "каталог не читается: "+err.Error())
		return
	}
	if !at.IsZero() {
		out.UpdatedAt = &at
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// TreeNode — раздел трекера для выбора разделов в настройках.
type TreeNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parentId"` // "" — верхний уровень
}

// SectionInfo — раздел во вкладке трекера: в каталоге есть его раздачи.
type SectionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	Group     string `json:"group"`     // группа Rutracker (c2, c18, c20); у Rutor и вне групп — ""
	GroupName string `json:"groupName"` // «Кино», «Сериалы», «Документалистика»
}

// tracker — трекер из запроса; неизвестный — 404 и false.
func (c *Catalog) tracker(w http.ResponseWriter, name string) bool {
	if _, ok := c.sources[name]; !ok {
		httpx.WriteError(w, http.StatusNotFound, "трекера «"+name+"» нет")
		return false
	}
	return true
}

func (c *Catalog) handleTree(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tracker")
	if !c.tracker(w, name) {
		return
	}
	tree, err := c.st.tree(r.Context(), name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "дерево разделов не читается: "+err.Error())
		return
	}
	out := make([]TreeNode, len(tree))
	for i, n := range tree {
		out[i] = TreeNode{ID: n.ID, Name: n.Name, ParentID: n.ParentID}
	}
	if name == "rutracker" && r.URL.Query().Get("level") == "1" {
		// Экран «Разделы каталога» (спека 11b, 7.1): группы названиями пульта и их подразделы первого
		// уровня без служебных.
		out = out[:0]
		fl := firstLevel(tree)
		for _, g := range RutrackerGroups {
			out = append(out, TreeNode{ID: g.ID, Name: g.Name})
			for _, s := range fl[g.ID] {
				out = append(out, TreeNode{ID: s.ID, Name: s.Name, ParentID: g.ID})
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *Catalog) handleSections(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("tracker")
	if !c.tracker(w, name) {
		return
	}
	out, err := c.trackerSections(r.Context(), name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "разделы не читаются: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// trackerSections — разделы трекера, в которых есть раздачи, в порядке настройки и дерева. У Rutracker —
// с группой, в порядке групп и дерева (спека 11b, 7.1); раздел вне групп — в конце, без группы.
func (c *Catalog) trackerSections(ctx context.Context, tracker string) ([]SectionInfo, error) {
	cats, err := c.Categories(ctx)
	if err != nil {
		return nil, err
	}
	out := []SectionInfo{}
	for _, cat := range cats {
		if cat.Tracker == tracker && cat.Count > 0 {
			out = append(out, SectionInfo{ID: cat.ID, Name: cat.Name, Count: cat.Count})
		}
	}
	if tracker != "rutracker" {
		return out, nil
	}
	tree, err := c.st.tree(ctx, tracker)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{}
	group := map[string]Group{}
	fl := firstLevel(tree)
	for _, g := range RutrackerGroups {
		for _, s := range fl[g.ID] {
			rank[s.ID] = len(rank)
			group[s.ID] = g
		}
	}
	for i := range out {
		g := group[out[i].ID]
		out[i].Group, out[i].GroupName = g.ID, g.Name
	}
	slices.SortStableFunc(out, func(a, b SectionInfo) int {
		ra, oka := rank[a.ID]
		rb, okb := rank[b.ID]
		switch {
		case oka && okb:
			return ra - rb
		case oka:
			return -1
		case okb:
			return 1
		}
		return 0
	})
	return out, nil
}
