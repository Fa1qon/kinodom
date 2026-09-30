package catalog

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"kinodom/internal/httpx"
	"kinodom/internal/meta"
)

// kinopoiskIDs — номер Кинопоиска раздач: из описания, иначе найденный очередью рейтингов (спека этапа 7,
// раздел 10.4). Нет в ответе — номер не известен.
func (c *Catalog) kinopoiskIDs(ctx context.Context, rs []row) (map[int64]int, error) {
	out := make(map[int64]int, len(rs))
	var keys []string
	for _, r := range rs {
		if r.KinopoiskID > 0 {
			out[r.ID] = r.KinopoiskID
		} else {
			keys = append(keys, r.Tracker+":"+r.TopicID)
		}
	}
	if c.ratings == nil || len(keys) == 0 {
		return out, nil
	}
	ratings, err := c.ratings.For(ctx, keys)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if id := ratings[r.Tracker+":"+r.TopicID].KinopoiskID; r.KinopoiskID == 0 && id > 0 {
			out[r.ID] = id
		}
	}
	return out, nil
}

// workKeys — ключ произведения по названию раздачи: список каталога разбирает сотни названий на каждый запрос.
var workKeys sync.Map

// workKey — ключ произведения раздачи: название + год + фильм/сериал (спека 11b, 5.3).
func workKey(r row) string {
	if k, ok := workKeys.Load(r.Title); ok {
		return k.(string)
	}
	k := meta.WorkKey(meta.ParseTitle(r.Title))
	workKeys.Store(r.Title, k)
	return k
}

// films — одна карточка на произведение (спека 11b, 5.3): раздачи с одним номером Кинопоиска; раздача
// без номера, чей ключ произведения совпал с ключом раздачи с номером, — в карточку этого номера;
// остальные без номера — по ключу произведения (дубли уходят, ещё до того как номер найден). Из группы
// остаётся раздача в формате в приоритете с наибольшим числом раздающих, а если такой нет — с
// наибольшим числом раздающих. Порядок — по раздающим оставшихся. size — раздач в группе оставшейся.
func films(rs []row, kp map[int64]int, pref string) (out []row, size map[int64]int) {
	better := func(a, b row) bool { // a лучше b
		if pa, pb := prefers(a.Format, pref), prefers(b.Format, pref); pa != pb {
			return pa
		}
		return a.Seeders > b.Seeders
	}
	keys := make([]string, len(rs))
	kpOfWork := map[string]int{}
	for i, r := range rs {
		keys[i] = workKey(r)
		if id := kp[r.ID]; id > 0 && keys[i] != "" {
			if _, ok := kpOfWork[keys[i]]; !ok {
				kpOfWork[keys[i]] = id
			}
		}
	}
	best := map[string]int{} // группа → индекс в out
	count := map[string]int{}
	out = make([]row, 0, len(rs))
	for i, r := range rs {
		g := ""
		switch id := kp[r.ID]; {
		case id > 0:
			g = "kp:" + strconv.Itoa(id)
		case keys[i] != "" && kpOfWork[keys[i]] > 0:
			g = "kp:" + strconv.Itoa(kpOfWork[keys[i]])
		case keys[i] != "":
			g = "w:" + keys[i]
		default:
			g = "id:" + strconv.FormatInt(r.ID, 10)
		}
		count[g]++
		if j, ok := best[g]; ok {
			if better(r, out[j]) {
				out[j] = r
			}
			continue
		}
		best[g] = len(out)
		out = append(out, r)
	}
	size = make(map[int64]int, len(out))
	for g, j := range best {
		size[out[j].ID] = count[g]
	}
	slices.SortStableFunc(out, func(a, b row) int { return b.Seeders - a.Seeders })
	return out, size
}

// variantRows — живые раздачи фильмов kpIDs на обоих трекерах: по номеру из описания и по найденному
// очередью рейтингов. Раздача, у которой в описании другой номер, в чужой фильм не попадает; одинаковый
// infohash схлопнут. Порядок — формат в приоритете, потом раздающие.
func (c *Catalog) variantRows(ctx context.Context, kpIDs []int) (map[int][]row, error) {
	byKP := map[int][]row{}
	if len(kpIDs) == 0 {
		return byKP, nil
	}
	own, err := c.st.rowsByKinopoisk(ctx, kpIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range own {
		byKP[r.KinopoiskID] = append(byKP[r.KinopoiskID], r)
	}
	if c.ratings != nil {
		links, err := c.ratings.Releases(ctx, kpIDs)
		if err != nil {
			return nil, err
		}
		kpOf := map[string]int{}
		var keys []string
		for id, ks := range links {
			for _, k := range ks {
				kpOf[k] = id
				keys = append(keys, k)
			}
		}
		found, err := c.st.rowsByKeys(ctx, keys)
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			if r.KinopoiskID != 0 { // номер в описании — раздача уже учтена по нему или это другой фильм
				continue
			}
			id := kpOf[r.Tracker+":"+r.TopicID]
			byKP[id] = append(byKP[id], r)
		}
	}
	pref := c.PreferredFormat()
	for id, rs := range byKP {
		rs = collapse(rs)
		preferFirst(rs, pref)
		byKP[id] = rs
	}
	return byKP, nil
}

// Variants — раздачи того же фильма на обоих трекерах, включая эту (спека этапа 7, раздел 10.4). Без
// номера Кинопоиска — только эта раздача.
func (c *Catalog) Variants(ctx context.Context, id int64) ([]Entry, error) {
	_, rs, err := c.variantsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.entries(ctx, rs)
}

// variantsOf — раздача id и раздачи того же фильма, включая её.
func (c *Catalog) variantsOf(ctx context.Context, id int64) (row, []row, error) {
	byID, err := c.st.rowsByID(ctx, []int64{id})
	if err != nil {
		return row{}, nil, err
	}
	r, ok := byID[id]
	if !ok {
		return row{}, nil, ErrNoRelease
	}
	kp, err := c.kinopoiskIDs(ctx, []row{r})
	if err != nil {
		return row{}, nil, err
	}
	rs := []row{r}
	if film := kp[r.ID]; film > 0 {
		vs, err := c.variantRows(ctx, []int{film})
		if err != nil {
			return row{}, nil, err
		}
		same, err := c.unnumberedOfWork(ctx, r.Tracker, append(vs[film], r))
		if err != nil {
			return row{}, nil, err
		}
		all := collapse(append(vs[film], same...))
		preferFirst(all, c.PreferredFormat())
		rs = withCurrent(all, r, c.PreferredFormat())
	} else if wk := workKey(r); wk != "" {
		// Без номера — раздачи того же произведения в каталоге этого трекера (спека 11b, 5.3).
		all, err := c.st.catalogRows(ctx, c.enabled())
		if err != nil {
			return row{}, nil, err
		}
		var same []row
		for _, x := range all {
			if x.Tracker == r.Tracker && workKey(x) == wk {
				same = append(same, x)
			}
		}
		same = collapse(same)
		preferFirst(same, c.PreferredFormat())
		rs = withCurrent(same, r, c.PreferredFormat())
	}
	return r, rs, nil
}

// unnumberedOfWork — раздачи каталога трекера без номера Кинопоиска с тем же ключом произведения, что у
// раздач group этого трекера: films() склеивает их в карточку номера, и «Другие раздачи» её должны
// показывать, пока очередь рейтингов номер им не дала (ревью 11b-Б, Important 4).
func (c *Catalog) unnumberedOfWork(ctx context.Context, tracker string, group []row) ([]row, error) {
	keys := map[string]bool{}
	in := map[int64]bool{}
	for _, x := range group {
		in[x.ID] = true
		if x.Tracker == tracker {
			if wk := workKey(x); wk != "" {
				keys[wk] = true
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	all, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return nil, err
	}
	var cand []row
	for _, x := range all {
		if x.Tracker == tracker && x.KinopoiskID == 0 && !in[x.ID] && keys[workKey(x)] {
			in[x.ID] = true
			cand = append(cand, x)
		}
	}
	kp, err := c.kinopoiskIDs(ctx, cand)
	if err != nil {
		return nil, err
	}
	var out []row
	for _, x := range cand {
		if kp[x.ID] == 0 {
			out = append(out, x)
		}
	}
	return out, nil
}

// withCurrent — открытая раздача r в списке раздач фильма: пульт отмечает в нём текущую. Если её
// схлопнул двойник с другого трекера (одинаковый infohash, раздающих больше), вместо двойника — она
// сама, и строк столько же, сколько «N раздач» на карточке. Нет и двойника (сняли с трекера) — в конце.
func withCurrent(rs []row, r row, pref string) []row {
	if slices.ContainsFunc(rs, func(x row) bool { return x.ID == r.ID }) {
		return rs
	}
	i := slices.IndexFunc(rs, func(x row) bool { return r.InfoHash != "" && x.InfoHash == r.InfoHash })
	if i < 0 {
		return append(rs, r)
	}
	rs[i] = r
	slices.SortStableFunc(rs, func(a, b row) int { return b.Seeders - a.Seeders })
	preferFirst(rs, pref)
	return rs
}

// VariantsView — «Другие раздачи» для API.
type VariantsView struct {
	Items  []EntryView     `json:"items"`
	Search *VariantsSearch `json:"search"` // null — на трекерах не искали
}

// VariantsSearch — поиск других раздач на трекерах: как у GET /search, пульт повторяет запрос, пока
// complete = false (спека этапа 7, раздел 10.5).
type VariantsSearch struct {
	Complete bool              `json:"complete"`
	Trackers map[string]string `json:"trackers"` // трекер → «ok», «идёт» или текст ошибки
}

// handleVariants — «Другие раздачи» на экране раздачи (спека этапа 7, раздел 10.4); search=1 — и поиск
// на трекерах (раздел 10.5), poll=1 — повторный опрос того же поиска.
func (c *Catalog) handleVariants(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер раздачи")
		return
	}
	var es []Entry
	var search *VariantsSearch
	if r.URL.Query().Get("search") == "1" {
		var st SearchState
		es, st, err = c.SearchVariants(r.Context(), id, r.URL.Query().Get("poll") == "1")
		search = &VariantsSearch{Complete: st.Complete, Trackers: st.Trackers}
	} else {
		es, err = c.Variants(r.Context(), id)
	}
	switch {
	case errors.Is(err, ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, errNoTitle):
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "раздачи фильма не читаются: "+err.Error())
		return
	}
	out := VariantsView{Items: make([]EntryView, len(es)), Search: search}
	for i, e := range es {
		out.Items[i] = e.View()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
