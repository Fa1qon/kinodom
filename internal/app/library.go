package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"kinodom/internal/history"
	"kinodom/internal/library"
	"kinodom/internal/meta"
	"kinodom/internal/recs"
	"kinodom/internal/torrents"
)

// initLibrary — медиатека (спека этапа 9): скачанное в Kinodom и папки заказчика одними карточками.
func (a *App) initLibrary(ctx context.Context) {
	// Кинопоиск без токена, ключ — запасной (спека 11b, 5.6); вид фильма — из базы рейтингов.
	kp := &meta.KPAny{Web: a.kpweb, Key: a.kp, Types: func(ctx context.Context, id int) (string, error) {
		fs, err := a.Ratings.Films(ctx, []int{id})
		return fs[id].Type, err
	}}
	o := library.Options{DB: a.DB, KP: kp, KPPoster: a.kp.PosterURL, Ratings: a.Ratings, Downloads: libraryDownloads{a}, History: a.History, Power: a.Power,
		CanDelete:    a.canDelete,
		Removed:      func(ctx context.Context, hash string) { a.History.MarkEnded(ctx, hash, -1, history.EndedDeleted, "") },
		DownloadsDir: func() string { return a.Settings.Current().DownloadsDir },
		KeepDays:     func() int { return a.Settings.Current().KeepDays },
		TorrentFolders: func(ctx context.Context) ([]string, error) { // скачанное в папку медиатеки — единицей раздачи (план 14В)
			if a.Torrents != nil && a.Torrents.Engine() != nil {
				return a.Torrents.Folders(ctx) // и открытые раздачи, чья метаинфо ещё не в базе
			}
			// Движок ещё не поднялся (первый обход при запуске) — папки раздач по реестру (ревью 14В).
			return torrents.NewRegistry(a.DB).Folders(ctx, a.Settings.Current().DownloadsDir)
		},
		Catalog: func() library.Catalog { // каталог пакета рекомендаций: опознание без Кинопоиска
			if a.Recs == nil {
				return nil
			}
			if m := a.Recs.Model(); m != nil {
				return recsCatalog{m}
			}
			return nil
		},
		Log: a.Log.With("module", "library")}
	if a.Images != nil {
		o.Posters = a.Images
	}
	a.Library = library.New(o)
	a.Library.Register(a.API)
	a.API.HandleLocal("GET /api/v1/library/access", a.Library.Name(), http.HandlerFunc(a.handleAccess)) // «Разрешить доступ» (этап 11a)
	a.Sup.Add(a.Library, a.ModuleEnabled(ctx, a.Library.Name()))
}

// errNoDownloads — загрузки не работают (движок не запустился): медиатека не трогает единицы
// скачанного, пока они не вернутся.
var errNoDownloads = errors.New("загрузки не работают")

// libraryDownloads — скачанное для медиатеки: раздачи с хранимыми файлами и их страницы в каталоге.
type libraryDownloads struct{ a *App }

func (d libraryDownloads) TorrentUnits(ctx context.Context) ([]library.TorrentUnit, error) {
	a := d.a
	if a.Torrents == nil || a.Torrents.Engine() == nil {
		return nil, errNoDownloads
	}
	ts, err := a.Torrents.LibraryTorrents(ctx)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(ts))
	for i, t := range ts {
		hashes[i] = t.Hash
	}
	refs, err := a.Catalog.ReleasesByHash(ctx, hashes)
	if err != nil {
		return nil, err
	}
	out := make([]library.TorrentUnit, 0, len(ts))
	for _, t := range ts {
		u := library.TorrentUnit{Hash: t.Hash, Name: t.Name, Dir: t.Dir, LastOpened: t.LastOpened, Missing: t.Missing}
		for _, f := range t.Files {
			u.Files = append(u.Files, library.TorrentFile{Index: f.Index, Path: f.Name, Size: f.Size, Done: f.Done, Stored: f.Stored,
				Readiness: string(f.Readiness)})
		}
		if ref, ok := refs[t.Hash]; ok {
			if rel, err := a.Catalog.Release(ctx, ref.ID); err == nil {
				pt := meta.ParseTitle(rel.Title)
				u.Release = &library.ReleaseData{KP: rel.Rating.KinopoiskID, Title: rel.Title, NameRu: pt.Ru, NameOrig: pt.Orig,
					Year: pt.Year, Description: rel.Description, ImageKey: rel.ImageKey, Quality: ref.Quality}
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// recsCatalog — каталог пакета рекомендаций для медиатеки; новый пакет — другое значение, индекс заново.
type recsCatalog struct{ m *recs.Model }

func (c recsCatalog) EachTitle(ctx context.Context, fn func(meta.Film)) error {
	return c.m.Titles(ctx, func(f recs.Film) {
		fn(meta.Film{ID: f.KP, NameRu: f.Title, NameOrig: f.TitleOrig, Year: f.Year, Type: kpType(f.Kind)})
	})
}

func (c recsCatalog) Details(ctx context.Context, id int) (meta.FilmDetails, bool) {
	f, ok := c.m.Film(ctx, id)
	if !ok || f.Description == "" || f.Year == 0 {
		return meta.FilmDetails{}, false
	}
	d := meta.FilmDetails{Film: meta.Film{ID: f.KP, NameRu: f.Title, NameOrig: f.TitleOrig, Year: f.Year, Type: kpType(f.Kind),
		Rating: f.RatingKP, RatingIMDb: f.RatingIMDb}, Description: f.Description, PosterURL: f.Poster, Genres: []string{}}
	for _, g := range strings.Split(f.Genres, ",") {
		if g = strings.TrimSpace(g); g != "" {
			d.Genres = append(d.Genres, g)
		}
	}
	return d, true
}

// kpType — вид фильма пакета типом Кинопоиска.
func kpType(kind string) string {
	if kind == "series" {
		return "TV_SERIES"
	}
	return "FILM"
}
