// Package rutortest — страницы Rutor, снятые вживую 2026-09-28 (spikes/misc/testdata/rutor),
// и фейковый Rutor на них. Только для тестов.
package rutortest

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata
var files embed.FS

// Page — образец testdata/<name>.
func Page(t testing.TB, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ListPages — образцы со списками раздач: вместе 1289 строк (так же разобрано в spikes/misc).
var ListPages = []string{
	"browse_12_sort2.html", "browse_12_sort2_page2.html", "browse_1_sort2.html",
	"cat_nauchno_popularnoe.html", "index.html", "mirror_rutor_is.html", "top.html",
	"search_all_matrix.html", "search_all_matrix_1999.html", "search_cat12_discovery.html",
}

// Server — фейковый Rutor на образцах: Mirror — сайт (зеркало), Download — d.rutor.info.
//
//	/browse/0/12/0/2               → browse_12_sort2.html (другие категории — browse_1_sort2.html)
//	/search/0/{кат}/100/2/{запрос} → 1 и 5 — search_all_matrix.html, 12 — search_cat12_discovery.html, иначе пусто
//	/torrent/{id}                  → torrent_{id}.html; нет образца — редирект на /d.php, как у Rutor
//	Download: /download/{id}       → download_{id}.torrent; нет образца — редирект на Mirror/d.php
type Server struct {
	Mirror, Download *httptest.Server
	// Override, если задан, отвечает раньше образцов; true — ответ уже дан.
	Override func(w http.ResponseWriter, r *http.Request) bool

	mu    sync.Mutex
	paths []string
}

func NewServer(t testing.TB) *Server {
	t.Helper()
	s := &Server{}
	s.Mirror = httptest.NewServer(http.HandlerFunc(s.site))
	t.Cleanup(s.Mirror.Close)
	s.Download = httptest.NewServer(http.HandlerFunc(s.download))
	t.Cleanup(s.Download.Close)
	return s
}

// Paths — запрошенные пути (раскодированные) по порядку, с обоих серверов.
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

func (s *Server) record(r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()
}

func (s *Server) site(w http.ResponseWriter, r *http.Request) {
	s.record(r)
	if s.Override != nil && s.Override(w, r) {
		return
	}
	p := r.URL.Path
	switch {
	case p == "/d.php":
		serve(w, "torrent_notfound.html")
	case p == "/browse/0/12/0/2":
		serve(w, "browse_12_sort2.html")
	case strings.HasPrefix(p, "/browse/"):
		serve(w, "browse_1_sort2.html")
	case strings.HasPrefix(p, "/search/"):
		parts := strings.SplitN(p, "/", 7) // "", "search", "0", кат, "100", "2", запрос
		cat := ""
		if len(parts) == 7 {
			cat = parts[3]
		}
		switch cat {
		case "1", "5":
			serve(w, "search_all_matrix.html")
		case "12":
			serve(w, "search_cat12_discovery.html")
		default:
			serve(w, "search_empty.html")
		}
	case strings.HasPrefix(p, "/torrent/"):
		name := "torrent_" + strings.TrimPrefix(p, "/torrent/") + ".html"
		if _, err := fs.Stat(files, "testdata/"+name); err != nil {
			http.Redirect(w, r, "/d.php", http.StatusFound)
			return
		}
		serve(w, name)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	s.record(r)
	if s.Override != nil && s.Override(w, r) {
		return
	}
	b, err := files.ReadFile("testdata/download_" + strings.TrimPrefix(r.URL.Path, "/download/") + ".torrent")
	if err != nil {
		http.Redirect(w, r, s.Mirror.URL+"/d.php", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Write(b)
}

func serve(w http.ResponseWriter, name string) {
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Write(b)
}
