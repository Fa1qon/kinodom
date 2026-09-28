package torrents

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anacrolix/torrent/metainfo"
)

func TestSanitizeComponent(t *testing.T) {
	cases := []struct{ in, want string }{
		{`a<b>c:d"e/f\g|h?i*j`, "a_b_c_d_e_f_g_h_i_j"},
		{"Космос: Пространство и время", "Космос_ Пространство и время"},
		{"con", "_con"},
		{"CON.mkv", "_CON.mkv"},
		{"имя. ", "имя"},
		{"", "_"},
		{"tab\there", "tab_here"},
	}
	for _, c := range cases {
		if got := sanitizeComponent(c.in); got != c.want {
			t.Errorf("sanitizeComponent(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeComponentTruncatesKeepingExtension(t *testing.T) {
	long := strings.Repeat("Очень длинное название ", 20) + ".mkv"
	got := sanitizeComponent(long)
	if n := utf8.RuneCountInString(got); n > maxComponentRunes {
		t.Fatalf("длина %d > %d", n, maxComponentRunes)
	}
	if !strings.HasSuffix(got, ".mkv") || strings.HasSuffix(strings.TrimSuffix(got, ".mkv"), " ") {
		t.Fatalf("расширение потеряно или перед ним пробел: %q", got)
	}
}

func TestTorrentDirAndEnginePath(t *testing.T) {
	var ih metainfo.Hash
	if err := ih.FromHexString("0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	info := &metainfo.Info{
		Name:  "Космос / Cosmos (2014)",
		Files: []metainfo.FileInfo{{Path: []string{"Сезон 1", "Серия 1: Начало.mkv"}, Length: 10}},
	}
	dir := torrentDir(`D:\K`, info, ih)
	if want := `D:\K\Космос _ Cosmos (2014) [01234567]`; dir != want {
		t.Fatalf("torrentDir = %q, ожидалось %q", dir, want)
	}
	p := enginePath(`D:\K`, info, ih, info.Files[0])
	if want := dir + `\Сезон 1\Серия 1_ Начало.mkv`; p != want {
		t.Fatalf("enginePath = %q, ожидалось %q", p, want)
	}
}

// Два длинных имени, отличающиеся только концом (номер серии), не должны склеиться в один файл.
func TestLongNamesDifferingOnlyInTailStayDistinct(t *testing.T) {
	prefix := strings.Repeat("Очень длинное название сериала ", 4)
	a := sanitizeComponent(prefix + "- 01.mkv")
	b := sanitizeComponent(prefix + "- 02.mkv")
	if a == b {
		t.Fatalf("серии склеились в одно имя: %q", a)
	}
	for _, s := range []string{a, b} {
		if utf8.RuneCountInString(s) > maxComponentRunes || !strings.HasSuffix(s, ".mkv") {
			t.Fatalf("неверное имя: %q", s)
		}
	}
}
