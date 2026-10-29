package app

import (
	"testing"

	"kinodom/internal/catalog"
	"kinodom/internal/playback"
	"kinodom/internal/torrents"
)

func seasonRel(hash string, season, from, to int, files ...int) seasonFiles {
	x := seasonFiles{rel: catalog.SeriesRelease{InfoHash: hash, Season: season, EpisodeFrom: from, EpisodeTo: to}}
	for _, i := range files {
		x.files = append(x.files, torrents.FileInfo{Index: i, Name: "e" + string(rune('0'+i)) + ".mkv"})
	}
	return x
}

// ownPlaylist — список серий текущей раздачи, как его отдаёт PlaySource.
func ownPlaylist(hash string, files ...int) []playback.Choice {
	var out []playback.Choice
	for _, i := range files {
		out = append(out, playback.Choice{Ref: playback.Ref{Kind: "torrent", Hash: hash, Index: i}})
	}
	return out
}

func hashes(cs []playback.Choice) []string {
	var out []string
	for _, c := range cs {
		if len(out) == 0 || out[len(out)-1] != c.Ref.Hash {
			out = append(out, c.Ref.Hash)
		}
	}
	return out
}

// Ревью 2026-10-29: последняя серия S2 при открытых S1 и S2 — «Следующей» нет, а меню идёт S1, S2, S3,
// а не S2, S1, S3.
func TestLinkSeasonsOrder(t *testing.T) {
	all := []seasonFiles{seasonRel("s1", 1, 1, 8, 0, 1), seasonRel("s2", 2, 1, 8, 0, 1), seasonRel("s3", 3, 1, 8, 0, 1)}
	src := playback.Source{Playlist: ownPlaylist("s2", 0, 1)}
	linkSeasons(playback.Ref{Kind: "torrent", Hash: "s2", Index: 1}, all, &src)
	if got := hashes(src.Playlist); len(got) != 3 || got[0] != "s1" || got[1] != "s2" || got[2] != "s3" {
		t.Fatalf("меню по порядку сезонов: %v", got)
	}
	if src.Prev == nil || src.Prev.Hash != "s1" || src.Prev.Index != 1 || src.Next == nil || src.Next.Hash != "s3" || src.Next.Index != 0 {
		t.Fatalf("соседи: %+v %+v", src.Prev, src.Next)
	}
	src = playback.Source{Playlist: ownPlaylist("s2", 0, 1)}
	linkSeasons(playback.Ref{Kind: "torrent", Hash: "s2"}, all[:2], &src)
	if src.Next != nil {
		t.Fatalf("после последнего открытого сезона «Следующей» нет: %+v", src.Next)
	}
}

// Раздачи по одной серии одного сезона («S04 (01 серия)», «S04 (02 серия)») связываются по первой серии.
func TestLinkSeasonsSplitSeason(t *testing.T) {
	all := []seasonFiles{seasonRel("e1", 4, 1, 1, 0), seasonRel("e2", 4, 2, 2, 0), seasonRel("e2hd", 4, 2, 2, 0)}
	src := playback.Source{Playlist: ownPlaylist("e1", 0)}
	linkSeasons(playback.Ref{Kind: "torrent", Hash: "e1"}, all, &src)
	if src.Next == nil || src.Next.Hash != "e2" {
		t.Fatalf("следующая серия — из соседней раздачи: %+v", src.Next)
	}
	if got := hashes(src.Playlist); len(got) != 2 {
		t.Fatalf("та же серия в другом качестве не повторяется в меню: %v", got)
	}
}

// Пак без номера сезона не становится «предыдущим сезоном» первого.
func TestLinkSeasonsSkipsUnnumbered(t *testing.T) {
	all := []seasonFiles{seasonRel("pack", 0, 0, 0, 0, 1, 2), seasonRel("s1", 1, 1, 8, 0, 1)}
	src := playback.Source{Playlist: ownPlaylist("s1", 0, 1)}
	linkSeasons(playback.Ref{Kind: "torrent", Hash: "s1"}, all, &src)
	if src.Prev != nil || len(hashes(src.Playlist)) != 1 {
		t.Fatalf("пак без сезона: prev %+v, меню %v", src.Prev, hashes(src.Playlist))
	}
}
