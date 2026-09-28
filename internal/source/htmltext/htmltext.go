// Package htmltext — текст со страниц трекеров: без разметки, с переводами строк там, где их
// видно на странице, и с одиночными пробелами.
package htmltext

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

var reInt = regexp.MustCompile(`\d+`)

// Clean — текст с одиночными пробелами (strings.Fields режет и по неразрывному пробелу).
func Clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// blocks — элементы, которые на странице начинаются с новой строки.
var blocks = map[string]bool{
	"div": true, "p": true, "tr": true, "li": true, "ul": true, "ol": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"pre": true, "center": true, "blockquote": true, "hr": true,
}

// Text — текст блока без разметки: <br> и блоки — переводы строк, больше одной пустой строки
// подряд не бывает, подписи без значения в конце убраны. drop — CSS-селектор того, что
// выбросить целиком (спойлеры, скрипты); выброшенный блок оставляет перевод строки.
func Text(sel *goquery.Selection, drop string) string {
	c := sel.Clone()
	if drop != "" {
		c.Find(drop).Each(func(_ int, s *goquery.Selection) {
			if blocks[goquery.NodeName(s)] {
				s.ReplaceWithHtml("<div></div>")
			} else {
				s.Remove()
			}
		})
	}
	var b strings.Builder
	for _, n := range c.Nodes {
		writeText(&b, n)
	}
	return dropTrailingLabels(tidy(b.String()))
}

// FirstInt — первое число в тексте; 0 — чисел нет.
func FirstInt(s string) int {
	n, _ := strconv.Atoi(reInt.FindString(s))
	return n
}

// FirstMatch — первая подгруппа re в атрибуте attr у элементов sel.
func FirstMatch(re *regexp.Regexp, sel *goquery.Selection, attr string) string {
	var out string
	sel.EachWithBreak(func(_ int, s *goquery.Selection) bool {
		v, _ := s.Attr(attr)
		if m := re.FindStringSubmatch(v); m != nil {
			out = m[1]
			return false
		}
		return true
	})
	return out
}

func writeText(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		// Переводы строк внутри HTML — просто пробелы; строки задают <br> и блоки.
		b.WriteString(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(n.Data))
		return
	case html.ElementNode:
		switch {
		case n.Data == "br":
			b.WriteString("\n")
			return
		case blocks[n.Data]:
			lineBreak(b)
			defer lineBreak(b)
		case n.Data == "td" || n.Data == "th":
			space(b) // ячейки таблицы — через пробел: «Год: 1999», а не «Год:1999»
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		writeText(b, ch)
	}
}

// lineBreak — перевод строки, если строка ещё не закончена: блоки подряд не дают пустых строк
// (пустую строку ставит только <br><br>).
func lineBreak(b *strings.Builder) {
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
}

func space(b *strings.Builder) {
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, " ") {
		b.WriteString(" ")
	}
}

// dropTrailingLabels убирает в конце подписи без значения («Релиз от:», «Скриншоты:»): их
// значения — картинки, а картинки в текст не попадают.
func dropTrailingLabels(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 {
		if last := lines[len(lines)-1]; last != "" && !strings.HasSuffix(last, ":") {
			break
		}
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// tidy — пробелы внутри строк схлопнуты, больше одной пустой строки подряд не бывает.
func tidy(s string) string {
	var out []string
	blank := true // пустые строки в начале не нужны
	for _, line := range strings.Split(s, "\n") {
		line = Clean(line)
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
