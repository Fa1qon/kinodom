package torrents

import (
	"slices"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	lists := [][]string{
		{"Серия 1.mkv", "Серия 2.mkv", "Серия 10.mkv", "серия 11.mkv"},
		{"S01E02.mkv", "S01E10.mkv", "S02E01.mkv"},
		{"a", "a1", "a2b", "a10"},
	}
	for _, l := range lists {
		for i := range l {
			for j := i + 1; j < len(l); j++ {
				if !naturalLess(l[i], l[j]) || naturalLess(l[j], l[i]) {
					t.Errorf("ожидалось %q < %q", l[i], l[j])
				}
			}
		}
	}
}

func TestPlayableFiles(t *testing.T) {
	all := []FileInfo{
		{0, "Серия 10.mkv", 900 << 20},
		{1, "Серия 2.mkv", 850 << 20},
		{2, "sample.mkv", 20 << 20},
		{3, "Sample/Серия 3 sample.mkv", 30 << 20},
		{4, "Серия 1.nfo", 1 << 10},
		{5, "Серия 1.srt", 50 << 10},
		{6, "Трейлер.mkv", 10 << 20}, // меньше 5 % от 900 МБ
		{7, "Серия 1.MKV", 880 << 20},
		{8, "Samples of nature.mkv", 870 << 20}, // «samples» — не «sample»
	}
	var got []int
	for _, f := range playableFiles(all) {
		got = append(got, f.Index)
	}
	if want := []int{8, 7, 1, 0}; !slices.Equal(got, want) {
		t.Fatalf("получено %v, ожидалось %v", got, want)
	}
}

func TestPlayableFilesEmpty(t *testing.T) {
	if got := playableFiles([]FileInfo{{0, "readme.txt", 10}}); got == nil || len(got) != 0 {
		t.Fatalf("ожидался пустой (не nil) список: %#v", got)
	}
}
