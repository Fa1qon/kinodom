package app

import (
	"context"
	"errors"

	"kinodom/internal/library"
	"kinodom/internal/meta"
)

// initLibrary — медиатека (спека этапа 9): скачанное в Kinodom и папки заказчика одними карточками.
func (a *App) initLibrary(ctx context.Context) {
	o := library.Options{DB: a.DB, KP: a.kp, Ratings: a.Ratings, Downloads: libraryDownloads{a}, History: a.History, Power: a.Power,
		DownloadsDir: func() string { return a.Settings.Current().DownloadsDir },
		KeepDays:     func() int { return a.Settings.Current().KeepDays },
		Log:          a.Log.With("module", "library")}
	if a.Images != nil {
		o.Posters = a.Images
	}
	a.Library = library.New(o)
	a.Library.Register(a.API)
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
