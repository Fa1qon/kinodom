package torrents

import (
	"context"
	"slices"
	"testing"

	"kinodom/internal/torrents/torrenttest"
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

// Список серий по метаинфо — ещё до открытия раздачи (у Rutor .torrent скачан заранее) и после,
// из сохранённой метаинфо (спека этапа 7, раздел 5.4).
func TestKnownFilesBeforeOpening(t *testing.T) {
	ctx := context.Background()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сезон 1", 64<<10,
		torrenttest.File{Path: "Серия 10.mkv", Size: 200_000}, torrenttest.File{Path: "Серия 2.mkv", Size: 200_000},
		torrenttest.File{Path: "sample.mkv", Size: 200_000}, torrenttest.File{Path: "обложка.jpg", Size: 1000})
	raw := torrentBytes(t, mi)
	fs, err := PlayableFiles(raw)
	if err != nil || len(fs) != 2 || fs[0].Name != "Серия 2.mkv" || fs[1].Name != "Серия 10.mkv" {
		t.Fatalf("серии: %+v, %v", fs, err)
	}
	if _, err := PlayableFiles([]byte("не торрент")); err == nil {
		t.Fatal("мусор принят за метаинфо")
	}
	s := newTestService(t)
	ih := mi.HashInfoBytes()
	if _, ok, err := s.KnownFiles(ctx, ih); ok || err != nil {
		t.Fatalf("неоткрытая раздача: %v, %v", ok, err)
	}
	if _, err := s.Open(ctx, Source{Torrent: raw}); err != nil {
		t.Fatal(err)
	}
	if fs, ok, _ := s.KnownFiles(ctx, ih); !ok || len(fs) != 2 {
		t.Fatalf("открытая раздача: %+v", fs)
	}
	must(t, s.reg.SaveMetainfo(ctx, ih, "Сезон 1", raw))
	fresh := serviceFor(newOfflineEngine(t), s.reg) // после перезапуска: раздача не открыта
	if fs, ok, _ := fresh.KnownFiles(ctx, ih); !ok || len(fs) != 2 {
		t.Fatalf("из сохранённой метаинфо: %+v", fs)
	}
}
