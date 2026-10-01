package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"kinodom/internal/source"
)

// deepList — весь список раздела по раздающим после обновления: из него — порции глубже первой сотни
// (спека 11b, 7.2). В памяти, до следующего обновления раздела.
type deepList struct {
	rs []source.Release
	at time.Time
}

// sectionTop — список раздела по раздающим как пришёл (защиты «все нули» смотрят на него). У Rutracker
// подраздел — все его видеофорумы (дерево из базы; раздела нет в дереве — он сам), весь список каждого
// из API склеивается; весь список раздела с раздающими запоминается для порций. У Rutor — первая страница.
func (c *Catalog) sectionTop(ctx context.Context, cat CategoryRef, src source.Source) ([]source.Release, error) {
	if cat.Tracker != "rutracker" {
		return src.Top(ctx, cat.ID, topSize)
	}
	tree, err := c.st.tree(ctx, cat.Tracker)
	if err != nil {
		return nil, dbError{err}
	}
	forums := videoForums(tree, cat.ID)
	if len(forums) == 0 {
		forums = []string{cat.ID}
	}
	var all []source.Release
	seen := map[string]bool{}
	for _, f := range forums {
		rs, err := src.Top(ctx, f, 0)
		if errors.Is(err, source.ErrNoSection) {
			// Форум без раздач (подборки ссылок) — подраздел обновляется без него (вживую 11b-Г).
			c.log.Info("каталог: форума нет в API — пропущен", "tracker", cat.Tracker, "section", cat.ID, "forum", f)
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if !seen[r.TopicID] {
				seen[r.TopicID] = true
				all = append(all, r)
			}
		}
	}
	slices.SortStableFunc(all, func(a, b source.Release) int { return b.Seeders - a.Seeders })
	c.mu.Lock()
	c.deep[cat] = deepList{rs: withSeeders(slices.Clone(all)), at: c.now()}
	c.mu.Unlock()
	return all, nil
}

// withSeeders — только раздачи, у которых есть раздающие.
func withSeeders(rs []source.Release) []source.Release {
	return slices.DeleteFunc(rs, func(r source.Release) bool { return r.Seeders <= 0 })
}

// sectionForums — форумы раздела для ленты новых раздач: у Rutracker — видеофорумы подраздела.
func (c *Catalog) sectionForums(ctx context.Context, cat CategoryRef) []string {
	if cat.Tracker != "rutracker" || strings.HasPrefix(cat.ID, "c") {
		return []string{cat.ID}
	}
	tree, err := c.st.tree(ctx, cat.Tracker)
	if err != nil {
		return []string{cat.ID}
	}
	if fs := videoForums(tree, cat.ID); len(fs) > 0 {
		return fs
	}
	return []string{cat.ID}
}

// pager — источник отдаёт страницы раздела (Rutor): порции глубже первой сотни.
type pager interface {
	TopPage(ctx context.Context, categoryID string, page int) ([]source.Release, bool, error)
}

// ensureOne — следующая порция раздела с трекера (спека 11b, 7.2): Rutracker — следующие 100 из списка
// раздела в памяти (после перезапуска — из API один раз), Rutor — следующая страница раздела. more — у
// трекера, возможно, есть ещё. Конец списка — у Rutor страница неполная, на ней раздачи без раздающих
// (список по раздающим — дальше одни нули) или две порции подряд не добавили ни одной раздачи (сайт
// повторяет последнюю страницу) — ревью 11b-Г. Курсор порций двигается только после удачного ответа;
// обновление раздела его сбрасывает.
func (c *Catalog) ensureOne(ctx context.Context, cat CategoryRef) (more bool, err error) {
	src, ok := c.sources[cat.Tracker]
	if !ok {
		return false, nil
	}
	count, err := c.st.sectionCount(ctx, cat)
	if err != nil {
		return false, dbError{err}
	}
	c.mu.Lock()
	end, pos := c.deepEnd[cat], c.deepPos[cat]
	_, havePos := c.deepPos[cat]
	c.mu.Unlock()
	if end {
		return false, nil
	}
	var rs []source.Release
	last := false
	if cat.Tracker == "rutracker" {
		c.mu.Lock()
		d, have := c.deep[cat]
		c.mu.Unlock()
		if !have {
			if _, err := c.sectionTop(ctx, cat, src); err != nil {
				return true, err
			}
			c.mu.Lock()
			d = c.deep[cat]
			c.mu.Unlock()
		}
		if !havePos {
			pos = count
		}
		from := min(pos, len(d.rs))
		to := min(from+topSize, len(d.rs))
		rs, last, pos = d.rs[from:to], to >= len(d.rs), to
	} else if p, ok := src.(pager); ok {
		if !havePos {
			pos = max(1, (count+topSize-1)/topSize)
		}
		got, more, err := p.TopPage(ctx, cat.ID, pos)
		if err != nil {
			return true, err
		}
		rs = withSeeders(slices.Clone(got))
		last = !more || len(got) == 0 || len(rs) < len(got)
		pos++
	} else {
		last = true
	}
	added, err := c.st.appendEntries(ctx, cat, rs, c.now())
	if err != nil {
		return false, dbError{err}
	}
	c.mu.Lock()
	c.deepPos[cat] = pos
	if added == 0 {
		c.deepEmpty[cat]++
	} else {
		c.deepEmpty[cat] = 0
	}
	if last || (cat.Tracker != "rutracker" && c.deepEmpty[cat] >= 2) {
		c.deepEnd[cat] = true
		last = true
	}
	c.mu.Unlock()
	return !last, nil
}

// deepEnded — у трекера список раздела кончился (порций больше нет).
func (c *Catalog) deepEnded(cat CategoryRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deepEnd[cat]
}

// resetDeep — раздел обновили: порции глубже первой сотни — заново.
func (c *Catalog) resetDeep(cat CategoryRef) {
	c.mu.Lock()
	delete(c.deepPos, cat)
	delete(c.deepEnd, cat)
	delete(c.deepEmpty, cat)
	c.mu.Unlock()
}
