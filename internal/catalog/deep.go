package catalog

import (
	"context"
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
