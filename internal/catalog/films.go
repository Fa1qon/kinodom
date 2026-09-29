package catalog

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"kinodom/internal/httpx"
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

// films — одна карточка на фильм: из раздач с одним номером Кинопоиска остаётся раздача в формате в
// приоритете с наибольшим числом раздающих, а если такой нет — с наибольшим числом раздающих. Раздачи
// без номера не склеиваются. Порядок — по раздающим оставшихся раздач.
func films(rs []row, kp map[int64]int, pref string) []row {
	better := func(a, b row) bool { // a лучше b
		if pa, pb := prefers(a.Format, pref), prefers(b.Format, pref); pa != pb {
			return pa
		}
		return a.Seeders > b.Seeders
	}
	best := map[int]int{} // номер Кинопоиска → индекс в out
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		id := kp[r.ID]
		if id == 0 {
			out = append(out, r)
			continue
		}
		if i, ok := best[id]; ok {
			if better(r, out[i]) {
				out[i] = r
			}
			continue
		}
		best[id] = len(out)
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b row) int { return b.Seeders - a.Seeders })
	return out
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
	byID, err := c.st.rowsByID(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	r, ok := byID[id]
	if !ok {
		return nil, ErrNoRelease
	}
	kp, err := c.kinopoiskIDs(ctx, []row{r})
	if err != nil {
		return nil, err
	}
	rs := []row{r}
	if film := kp[r.ID]; film > 0 {
		vs, err := c.variantRows(ctx, []int{film})
		if err != nil {
			return nil, err
		}
		rs = withCurrent(vs[film], r, c.PreferredFormat())
	}
	return c.entries(ctx, rs)
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
	Items []EntryView `json:"items"`
}

// handleVariants — «Другие раздачи» на экране раздачи (спека этапа 7, раздел 10.4).
func (c *Catalog) handleVariants(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер раздачи")
		return
	}
	es, err := c.Variants(r.Context(), id)
	switch {
	case errors.Is(err, ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "раздачи фильма не читаются: "+err.Error())
		return
	}
	out := VariantsView{Items: make([]EntryView, len(es))}
	for i, e := range es {
		out.Items[i] = e.View()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
