package catalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"kinodom/internal/source"
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

// DefaultSections — разделы по умолчанию (спека 11b, 7.1): у Rutracker — подразделы первого уровня со всеми
// видеоподфорумами.
var DefaultSections = func() []Section {
	out := make([]Section, len(DefaultCategories))
	for i, c := range DefaultCategories {
		out[i] = Section{Tracker: c.Tracker, ID: c.ID, All: c.Tracker == "rutracker"}
	}
	return out
}()

// NormalizeSections — выбор разделов по дереву Rutracker (спека 11b, 7.1): у Rutracker раздел каталога —
// подраздел первого уровня групп «Кино», «Сериалы», «Документалистика» со всеми видеоподфорумами («X+»):
// подфорум — его подраздел; «X» подраздела — «X+»; «cN+» — все подразделы группы; раздел в другой
// категории форума (спорт, музыка) — нет; раздела нет в дереве или над ним нет категории (дерева ещё нет) —
// как есть. Повторы убираются, порядок — первого появления. Rutor — как есть.
func NormalizeSections(ss []Section, tree []source.Category) []Section {
	parent := map[string]string{}
	for _, c := range tree {
		parent[c.ID] = c.ParentID
	}
	groups := firstLevel(tree)
	inGroups := func(id string) bool {
		return slices.ContainsFunc(RutrackerGroups, func(g Group) bool { return g.ID == id })
	}
	var out []Section
	add := func(s Section) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, s := range ss {
		if s.Tracker != "rutracker" || len(tree) == 0 {
			add(s)
			continue
		}
		if strings.HasPrefix(s.ID, "c") {
			for _, c := range groups[s.ID] {
				add(Section{s.Tracker, c.ID, true})
			}
			continue
		}
		if _, ok := parent[s.ID]; !ok {
			add(s) // дерево о нём не знает
			continue
		}
		// Вверх по дереву до категории: подраздел первого уровня — ребёнок категории на этом пути.
		id, top := s.ID, ""
		for {
			p := parent[id]
			if p == "" {
				break
			}
			if strings.HasPrefix(p, "c") {
				top = p
				break
			}
			id = p
		}
		switch {
		case top == "":
			add(s) // над разделом нет категории — как есть
		case inGroups(top) && !serviceForum(nameOf(tree, id)):
			add(Section{s.Tracker, id, true})
		}
	}
	return out
}

func nameOf(tree []source.Category, id string) string {
	for _, c := range tree {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

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

// expandSections — разделы каталога (что обновлять и что показывать): у Rutracker — подразделы первого
// уровня по NormalizeSections (их видеоподфорумы берёт обновление), у Rutor — разделы. Порядок — как в
// настройке; повторы убираются. Дерева Rutracker ещё нет (первый запуск без сети) — разделы как в
// настройке; категория без дерева не даёт ничего.
func (c *Catalog) expandSections(ctx context.Context, ss []Section) ([]CategoryRef, error) {
	tree, err := c.st.tree(ctx, "rutracker")
	if err != nil {
		return nil, err
	}
	var out []CategoryRef
	for _, s := range NormalizeSections(ss, tree) {
		ref := CategoryRef{s.Tracker, s.ID}
		if strings.HasPrefix(s.ID, "c") || slices.Contains(out, ref) {
			continue // у категорий Rutracker своих раздач нет
		}
		out = append(out, ref)
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
		if s.Tracker == "rutracker" && len(ids) > 0 {
			tree, err := c.st.tree(ctx, s.Tracker)
			if err != nil {
				return err
			}
			if len(NormalizeSections([]Section{s}, tree)) == 0 {
				return fmt.Errorf("Rutracker: раздел %s — не из групп «Кино», «Сериалы», «Документалистика»", s.ID)
			}
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
