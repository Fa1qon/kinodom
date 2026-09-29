package catalog

import (
	"context"
	"net/http"
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
}

// View — раздача для API.
func (e Entry) View() EntryView {
	t := meta.ParseTitle(e.Title)
	return EntryView{ID: e.ID, Tracker: e.Tracker, Title: e.Title, Name: t.Ru, Original: t.Orig, Year: t.Year,
		Quality: e.Quality, Category: e.Category, Seeders: e.Seeders, Leechers: e.Leechers, Size: e.Size,
		ImageKey: e.ImageKey, Kinopoisk: e.Rating.Kinopoisk, Format: e.Format, Season: t.Season, Variants: e.Variants}
}

// ListView — страница каталога трекера.
type ListView struct {
	Section   string      `json:"section"` // раздел, чья это страница
	Page      int         `json:"page"`
	Pages     int         `json:"pages"`     // страниц в разделе; общее число раздач пульт не показывает
	UpdatedAt *time.Time  `json:"updatedAt"` // последнее удачное обновление разделов трекера; null — ещё не было
	Entries   []EntryView `json:"entries"`
}

// handleList — страница раздела трекера по раздающим (спека этапа 7, раздел 5.4). Раздела в запросе
// нет — первый раздел трекера, где есть раздачи; страница вне диапазона — пустой список.
func (c *Catalog) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("tracker")
	if !c.tracker(w, name) {
		return
	}
	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	out := ListView{Section: q.Get("section"), Page: page, Pages: 1, Entries: []EntryView{}}
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
		es, total, err := c.List(r.Context(), ListOptions{Tracker: name, Category: out.Section, Offset: (page - 1) * PageSize, Limit: PageSize})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "каталог не читается: "+err.Error())
			return
		}
		out.Pages = max(1, (total+PageSize-1)/PageSize)
		for _, e := range es {
			out.Entries = append(out.Entries, e.View())
		}
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
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
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

// trackerSections — разделы трекера, в которых есть раздачи, в порядке настройки и дерева.
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
	return out, nil
}
