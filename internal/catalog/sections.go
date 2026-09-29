package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Section — раздел в настройке catalog.categories (спека этапа 7, раздел 5.4):
//
//	rutor:12        — категория Rutor;
//	rutracker:2110  — только сам раздел, без подразделов;
//	rutracker:46+   — раздел со всеми подразделами, в том числе появившимися позже;
//	rutracker:c20+  — вся категория Rutracker: своих раздач у категории нет, поэтому только с «+».
type Section struct {
	Tracker string
	ID      string // номер раздела; у категорий Rutracker — «c20»
	All     bool   // со всеми подразделами
}

func (s Section) String() string {
	if s.All {
		return s.Tracker + ":" + s.ID + "+"
	}
	return s.Tracker + ":" + s.ID
}

// DefaultSections — разделы по умолчанию (основная спека, раздел 6): каждый — без подразделов.
var DefaultSections = func() []Section {
	out := make([]Section, len(DefaultCategories))
	for i, c := range DefaultCategories {
		out[i] = Section{Tracker: c.Tracker, ID: c.ID}
	}
	return out
}()

// ParseSections — настройка catalog.categories: «rutracker:2110, rutracker:46+, rutor:12». Пусто —
// разделы по умолчанию. Строки этапов 5–6 («rutracker:2110, rutor:12») читаются так же, как раньше.
func ParseSections(s string) ([]Section, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultSections, nil
	}
	var out []Section
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		tracker, id, ok := strings.Cut(part, ":")
		if !ok || tracker == "" || id == "" {
			return nil, fmt.Errorf("раздел %q — нужно «трекер:номер», например rutracker:2110", part)
		}
		sec := Section{Tracker: tracker}
		sec.ID, sec.All = strings.CutSuffix(id, "+")
		num, isCat := strings.CutPrefix(sec.ID, "c")
		if _, err := strconv.Atoi(num); err != nil {
			return nil, fmt.Errorf("раздел %q — номер раздела не число", part)
		}
		switch {
		case isCat && tracker != "rutracker":
			return nil, fmt.Errorf("раздел %q — категории «c…» есть только у Rutracker", part)
		case isCat && !sec.All:
			return nil, fmt.Errorf("раздел %q — у категории нет своих раздач: нужно %s+", part, part)
		}
		out = append(out, sec)
	}
	return out, nil
}

// FormatSections — разделы строкой настройки catalog.categories.
func FormatSections(ss []Section) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = s.String()
	}
	return strings.Join(parts, ",")
}

// expandSections — разделы, чьи топы составляют каталог: записи с «+» раскрываются по дереву из
// таблицы categories. Порядок — как в настройке, подразделы — в порядке дерева; повторы убираются.
// Дерева трекера ещё нет (первый запуск без сети) — раздел с «+» пока только сам по себе; категория
// без дерева не даёт ничего.
func (c *Catalog) expandSections(ctx context.Context, ss []Section) ([]CategoryRef, error) {
	children := map[string]map[string][]string{} // трекер → родитель → дети в порядке дерева
	var out []CategoryRef
	seen := map[CategoryRef]bool{}
	add := func(tracker, id string) {
		ref := CategoryRef{tracker, id}
		if strings.HasPrefix(id, "c") || seen[ref] {
			return // у категорий Rutracker своих раздач нет
		}
		seen[ref] = true
		out = append(out, ref)
	}
	var walk func(tracker, id string)
	walk = func(tracker, id string) {
		add(tracker, id)
		for _, child := range children[tracker][id] {
			walk(tracker, child)
		}
	}
	for _, s := range ss {
		if !s.All {
			add(s.Tracker, s.ID)
			continue
		}
		if _, ok := children[s.Tracker]; !ok {
			tree, err := c.st.tree(ctx, s.Tracker)
			if err != nil {
				return nil, err
			}
			m := map[string][]string{}
			for _, n := range tree {
				m[n.ParentID] = append(m[n.ParentID], n.ID)
			}
			children[s.Tracker] = m
		}
		walk(s.Tracker, s.ID)
	}
	return out, nil
}

// CheckSections — разделы из пульта есть у трекеров: трекер известен, номер — в дереве разделов
// (если дерево уже загружено: без сети при первом запуске проверять не по чему).
func (c *Catalog) CheckSections(ctx context.Context, ss []Section) error {
	known := map[string]map[string]bool{}
	for _, s := range ss {
		if _, ok := c.sources[s.Tracker]; !ok {
			return fmt.Errorf("трекера «%s» нет", s.Tracker)
		}
		ids, ok := known[s.Tracker]
		if !ok {
			tree, err := c.st.tree(ctx, s.Tracker)
			if err != nil {
				return err
			}
			ids = map[string]bool{}
			for _, n := range tree {
				ids[n.ID] = true
			}
			known[s.Tracker] = ids
		}
		if len(ids) > 0 && !ids[s.ID] {
			return fmt.Errorf("%s: раздела %s нет в дереве разделов", title(s.Tracker), s.ID)
		}
	}
	return nil
}

// SetSections — разделы сменили в пульте: каталог сразу показывает только их, новые разделы
// обновляются в ближайший проход, не дожидаясь шести часов (у них ещё нет удачного обновления).
func (c *Catalog) SetSections(ctx context.Context, ss []Section) error {
	c.mu.Lock()
	c.sections = ss
	c.mu.Unlock()
	if err := c.reexpand(ctx); err != nil {
		return err
	}
	select {
	case c.sectionsChanged <- struct{}{}:
	default:
	}
	return nil
}

// reexpand — раскрыть разделы заново: дерево обновилось или разделы сменили.
func (c *Catalog) reexpand(ctx context.Context) error {
	c.mu.Lock()
	ss := c.sections
	c.mu.Unlock()
	cats, err := c.expandSections(ctx, ss)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cats = cats
	c.mu.Unlock()
	return nil
}

// enabled — разделы каталога сейчас (после раскрытия «+»).
func (c *Catalog) enabled() []CategoryRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cats
}
