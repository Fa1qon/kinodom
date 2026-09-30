package catalog

import (
	"strings"

	"kinodom/internal/source"
)

// Group — группа каталога Rutracker: категория форума и её название в пульте.
type Group struct{ ID, Name string }

// RutrackerGroups — три группы каталога Rutracker (решение заказчика, спека 11b, 7.1); остальные категории
// форума в каталог не входят.
var RutrackerGroups = []Group{{"c2", "Кино"}, {"c18", "Сериалы"}, {"c20", "Документалистика"}}

// serviceWords — по ним в названии подфорум служебный, а не видео: помощь, предложения, обсуждения,
// «Ищу / Предлагаю», звуковые дорожки, анонсы, правила, трейлеры (названия дерева 2026-09-28). Архивы —
// видео: там раздачи.
var serviceWords = []string{"помощь по разделу", "предложени", "обсуждени", "ищу", "звуковые дорожки", "анонс", "правила", "faq", "трейлер"}

// serviceForum — служебный подфорум по названию.
func serviceForum(name string) bool {
	n := strings.ToLower(name)
	for _, w := range serviceWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// firstLevel — подразделы первого уровня групп: форумы, чей родитель — категория группы, без служебных;
// в порядке дерева.
func firstLevel(tree []source.Category) map[string][]source.Category {
	out := map[string][]source.Category{}
	for _, g := range RutrackerGroups {
		for _, c := range tree {
			if c.ParentID == g.ID && !serviceForum(c.Name) {
				out[g.ID] = append(out[g.ID], c)
			}
		}
	}
	return out
}

// groupOf — группа подраздела первого уровня; "" — не подраздел групп.
func groupOf(tree []source.Category, id string) string {
	for g, cs := range firstLevel(tree) {
		for _, c := range cs {
			if c.ID == id {
				return g
			}
		}
	}
	return ""
}

// videoForums — форумы подраздела id: он сам и все его подфорумы (на любой глубине), кроме служебных;
// служебный подраздел — пусто.
func videoForums(tree []source.Category, id string) []string {
	byID := map[string]source.Category{}
	children := map[string][]string{}
	for _, c := range tree {
		byID[c.ID] = c
		children[c.ParentID] = append(children[c.ParentID], c.ID)
	}
	root, ok := byID[id]
	if !ok || serviceForum(root.Name) {
		return nil
	}
	out := []string{id}
	var walk func(string)
	walk = func(p string) {
		for _, ch := range children[p] {
			if serviceForum(byID[ch].Name) {
				continue
			}
			out = append(out, ch)
			walk(ch)
		}
	}
	walk(id)
	return out
}
