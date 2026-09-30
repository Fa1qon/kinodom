package catalog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"

	"kinodom/internal/meta"
)

// errNoTitle — у раздачи ещё нет названия (топ Rutracker без названий): искать не по чему.
var errNoTitle = errors.New("у раздачи ещё нет названия — искать другие раздачи не по чему")

// alternativesToEnrich — сколько найденных раздач без страницы догружать вне очереди: формат и номер
// Кинопоиска появляются со страницей раздачи (спека этапа 7, раздел 10.5).
const alternativesToEnrich = 10

// SearchVariants — «Другие раздачи» и найденные на трекерах раздачи того же фильма (спека этапа 7,
// раздел 10.5). Поиск — по первому названию и году, тот же, что GET /search, но без истории; пульт
// повторяет запрос, пока поиск не закончен и пока догружаются найденные. poll — такой повторный опрос:
// законченный с ошибкой трекера поиск он не запускает заново, иначе опросы искали бы на трекерах каждые
// 3 с. Найденные без страницы ставятся в догрузку вне очереди, не больше 10 по раздающим.
func (c *Catalog) SearchVariants(ctx context.Context, id int64, poll bool) ([]Entry, SearchState, error) {
	r, known, err := c.variantsOf(ctx, id)
	if err != nil {
		return nil, SearchState{}, err
	}
	t := meta.ParseTitle(r.Title)
	q := cmp.Or(t.Ru, t.Orig)
	if q == "" {
		return nil, SearchState{}, errNoTitle
	}
	if t.Year > 0 {
		q += " " + strconv.Itoa(t.Year)
	}
	run, q, err := c.startSearch(q, poll, false)
	if err != nil {
		return nil, SearchState{}, err
	}
	found, st, err := c.runRows(ctx, run)
	if err != nil {
		return nil, SearchState{}, err
	}
	st.Query = q
	found = collapse(found)
	kp, err := c.kinopoiskIDs(ctx, append([]row{r}, found...))
	if err != nil {
		return nil, SearchState{}, err
	}
	rs := slices.Clone(known)
	has := map[int64]bool{}
	for _, x := range known {
		has[x.ID] = true
	}
	pending := 0
	for _, f := range found {
		if has[f.ID] || !sameFilm(r, kp[r.ID], f, kp[f.ID]) {
			continue
		}
		has[f.ID] = true
		rs = append(rs, f)
		if f.DetailsAt.IsZero() && pending < alternativesToEnrich {
			c.enrichSoon(f.Tracker, f.ID)
			pending++
		}
	}
	pref := c.PreferredFormat()
	rs = collapse(rs)
	preferFirst(rs, pref)
	es, err := c.entries(ctx, withCurrent(rs, r, pref))
	return es, st, err
}

// sameFilm — найденная раздача f — тот же фильм, что a: номера Кинопоиска известны у обеих — они равны;
// иначе совпадает одно из названий (без учёта регистра, «ё» и знаков), а год отличается не больше
// чем на 1.
func sameFilm(a row, aKP int, f row, fKP int) bool {
	if aKP > 0 && fKP > 0 {
		return aKP == fKP
	}
	ta, tf := meta.ParseTitle(a.Title), meta.ParseTitle(f.Title)
	if ta.Year > 0 && tf.Year > 0 && (ta.Year-tf.Year > 1 || tf.Year-ta.Year > 1) {
		return false
	}
	names := map[string]bool{}
	for _, n := range ta.Names {
		names[meta.NormTitle(n)] = true
	}
	return slices.ContainsFunc(tf.Names, func(n string) bool { return names[meta.NormTitle(n)] })
}
