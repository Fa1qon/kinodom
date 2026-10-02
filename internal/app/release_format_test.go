package app

import (
	"testing"

	"kinodom/internal/torrents"
)

// Ревью 14А: после «Скачать» формат шапки — по файлам, а признак приоритета оставался от формата из описания.
// fileFormat пересчитывает оба вместе.
func TestFileFormatSetsPreferred(t *testing.T) {
	v := releaseView{Files: []torrents.FileInfo{{Index: 0, Name: "Фильм/Фильм.mkv", Size: 1 << 30}}}
	v.Format, v.Preferred = "AVI", false
	fileFormat(&v, "MKV")
	if v.Format != "MKV" || !v.Preferred {
		t.Fatalf("по файлам: %q, приоритет %v", v.Format, v.Preferred)
	}
	v.Files = []torrents.FileInfo{{Index: 0, Name: "Фильм.avi", Size: 1 << 30}}
	fileFormat(&v, "MKV")
	if v.Format != "AVI" || v.Preferred {
		t.Fatalf("AVI при MKV в приоритете: %q, приоритет %v", v.Format, v.Preferred)
	}
	none := releaseView{}
	none.Format, none.Preferred = "MKV", true
	fileFormat(&none, "MKV") // файлов ещё нет — как было
	if none.Format != "MKV" || !none.Preferred {
		t.Fatalf("без файлов: %q, приоритет %v", none.Format, none.Preferred)
	}
}
