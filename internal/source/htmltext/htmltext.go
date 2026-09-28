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

// Text — текст блока без разметки: <br> и блоки — переводы строк, больше одной пустой строки
// подряд не бывает. drop — CSS-селектор того, что выбросить целиком (спойлеры, скрипты).
func Text(sel *goquery.Selection, drop string) string {
	c := sel.Clone()
	if drop != "" {
		c.Find(drop).Remove()
	}
	var b strings.Builder
	for _, n := range c.Nodes {
		writeText(&b, n)
	}
	return tidy(b.String())
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
		switch n.Data {
		case "br":
			b.WriteString("\n")
			return
		case "div", "p", "tr", "li", "table", "h1", "h2", "h3", "pre":
			b.WriteString("\n")
			defer b.WriteString("\n")
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		writeText(b, ch)
	}
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
