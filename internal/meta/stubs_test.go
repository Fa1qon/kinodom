package meta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// План 14А, задача 4: заглушка imgbox «Thumbnail Temporarily Unavailable» (у заказчика 2026-10-01 вместо
// постера «Позывного Альфа») известна заранее: не постер с первого раза, а уже скачанная удаляется при
// запуске — каталог снимает ключ и берёт постер Кинопоиска.
func TestKnownStubs(t *testing.T) {
	if !slices.ContainsFunc(KnownStubs, func(s KnownStub) bool {
		return s.SHA256 == "c0ff95f9ec7fea007b8236e8efddfcc6c0dfdd56f8e4c38c8ffed8fde655d8a7" && s.Size == 8091
	}) {
		t.Fatal("заглушки imgbox нет среди известных")
	}
	stub := append(pngBytes(t), 7) // своя «заглушка» вместо картинки imgbox
	sum := sha256.Sum256(stub)
	dir := t.TempDir()
	cached := ImageKey("https://thumbs2.imgbox.com/aa/bb/x_t.jpg")
	if err := os.WriteFile(filepath.Join(dir, cached+".jpg"), stub, 0o644); err != nil {
		t.Fatal(err)
	}
	keep := ImageKey("https://example.org/p.png")
	if err := os.WriteFile(filepath.Join(dir, keep+".png"), pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(stub)
	}))
	t.Cleanup(srv.Close)
	im, err := NewImages(ImagesOptions{Dir: dir, Rate: 1000, AllowPrivate: true, StubSources: PosterStubSources,
		KnownStubs: []KnownStub{{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(stub))}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := im.Stubbed(); !slices.Equal(got, []string{cached}) {
		t.Fatalf("при запуске сняты %v, нужно [%s]", got, cached)
	}
	if im.find(cached) != "" || im.find(keep) == "" {
		t.Fatal("удалена не та картинка")
	}
	if _, err := im.Fetch(context.Background(), srv.URL+"/one.png", Direct); !errors.Is(err, ErrNoImage) {
		t.Fatalf("известная заглушка с первого адреса — не постер: %v", err)
	}
}

// План 14А, задача 4: миниатюра imgbox — качается оригинал (миниатюра бывает «Thumbnail Temporarily
// Unavailable»).
func TestPosterURLPrefersOriginal(t *testing.T) {
	cases := map[string]string{
		"https://thumbs2.imgbox.com/4f/3a/AbCdEf12_t.jpg": "https://images2.imgbox.com/4f/3a/AbCdEf12_o.jpg",
		"http://thumbs2.imgbox.com/4f/3a/AbCdEf12_t.png":  "https://images2.imgbox.com/4f/3a/AbCdEf12_o.png",
		"https://i128.fastpic.org/big/2026/0919/1a/x.jpg": "https://i128.fastpic.org/big/2026/0919/1a/x.jpg",
		"": "",
	}
	for in, want := range cases {
		if got := PosterURL(in); got != want {
			t.Errorf("PosterURL(%q) = %q, нужно %q", in, got, want)
		}
	}
}
