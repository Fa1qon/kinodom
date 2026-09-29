package catalog

import (
	"net/http"

	"kinodom/internal/httpx"
)

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

// Register — маршруты каталога (спека этапа 7, раздел 5.8).
func (c *Catalog) Register(r Router) {
	r.Handle("GET /api/v1/sources/{tracker}/categories", c.Name(), http.HandlerFunc(c.handleTree))
	r.Handle("GET /api/v1/catalog/sections", c.Name(), http.HandlerFunc(c.handleSections))
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
	cats, err := c.Categories(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "разделы не читаются: "+err.Error())
		return
	}
	out := []SectionInfo{}
	for _, cat := range cats {
		if cat.Tracker == name && cat.Count > 0 {
			out = append(out, SectionInfo{ID: cat.ID, Name: cat.Name, Count: cat.Count})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
