package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kinodom/internal/history"
	"kinodom/internal/httpx"
	"kinodom/internal/meta"
	"kinodom/internal/recs"
)

// initRecs — «Что посмотреть»: журнал вкуса в истории (кто смотрел, сколько, чем кончилось), отметки
// удаления из торрентов и медиатеки, рекомендации по пакету модели из папки данных.
func (a *App) initRecs() {
	a.History.SetOwner(a.recsOwner)
	a.History.SetFilmOf(a.filmOfHash)
	a.Torrents.SetRemovedHook(func(ctx context.Context, hash string, index int, how string) {
		if err := a.History.MarkEnded(ctx, hash, index, how, ""); err != nil {
			a.Log.Warn("журнал вкуса: отметка удаления не записалась", "hash", hash, "err", err)
		}
	})
	a.Recs = &recs.Service{DB: a.DB, Journal: resolvingJournal{a.History}, Wanted: a.recsWanted}
	// Пакет модели приходит с установщиком рядом с программой; поставленный вручную (POST /recs/model) —
	// в папке данных. Берётся более свежий: новый установщик не перекрывается старым ручным пакетом.
	path := filepath.Join(a.Paths.Data, "recs-model.db")
	paths := []string{path}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "recs-model.db"))
	}
	switch m, err := recs.OpenNewest(paths...); {
	case err == nil:
		a.Recs.SetModel(m)
		a.closers = append(a.closers, closerFunc(func() error { a.Recs.SetModel(nil); return nil }))
	case !errors.Is(err, recs.ErrNoModel):
		a.Log.Warn("пакет рекомендаций не открылся", "path", path, "err", err)
	}
	env := recs.Env{
		Owner:     func(r *http.Request) string { return a.History.Owner(r.Context(), httpx.Device(r)) },
		People:    a.recsPeople,
		KPKey:     func() string { return a.Settings.Current().KinopoiskKey },
		ModelPath: path,
	}
	if a.Images != nil {
		env.Posters = func(ctx context.Context, src string) (string, error) { return a.Images.Fetch(ctx, src, meta.Direct) }
	}
	a.Recs.Register(a.API, env)
	// Карточки фильмов вне пакета (пакет несёт только популярные) — с Кинопоиска без ключа, фоновой очередью:
	// каталог и правка в пульте идут раньше.
	if a.kpweb != nil {
		ctx, cancel := context.WithCancel(context.Background())
		a.closers = append(a.closers, closerFunc(func() error { cancel(); return nil }))
		a.Recs.StartCards(ctx, a.recsCard)
	}
}

// recsCard — карточка фильма для рекомендаций: фильм, а не нашёлся — сериал (и наоборот).
func (a *App) recsCard(ctx context.Context, kp int, series bool) (recs.Card, error) {
	d, err := a.kpweb.Details(ctx, meta.KPBackground, kp, series)
	if errors.Is(err, meta.ErrNotFound) {
		d, err = a.kpweb.Details(ctx, meta.KPBackground, kp, !series)
	}
	if errors.Is(err, meta.ErrNotFound) {
		return recs.Card{}, recs.ErrNoCard
	}
	if err != nil {
		return recs.Card{}, err
	}
	c := recs.Card{Kind: "film", Title: d.NameRu, TitleOrig: d.NameOrig, Year: d.Year, Description: d.Description,
		Poster: d.PosterURL, RatingKP: d.Rating, RatingIMDb: d.RatingIMDb, Genres: strings.ToLower(strings.Join(d.Genres, ", "))}
	switch d.Type {
	case "TV_SERIES", "MINI_SERIES", "TV_SHOW":
		c.Kind = "series"
	}
	return c, nil
}

// recsOwner — профиль семьи, на который переключено устройство: «p:<номер>»; нет — "".
func (a *App) recsOwner(ctx context.Context, device string) string {
	var id int64
	if err := a.DB.R.QueryRowContext(ctx, `SELECT profile_id FROM family_devices WHERE device = ?`, device).Scan(&id); err != nil {
		return ""
	}
	return "p:" + strconv.FormatInt(id, 10)
}

func (a *App) recsPeople(ctx context.Context) []recs.Person {
	out := []recs.Person{}
	rows, err := a.DB.R.QueryContext(ctx, `SELECT id, name FROM family_profiles ORDER BY name`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			out = append(out, recs.Person{Owner: fmt.Sprintf("p:%d", id), Name: name})
		}
	}
	return out
}

// filmOfHash — номер Кинопоиска раздачи или единицы медиатеки: единица медиатеки (распознанная или
// привязанная вручную), страница раздачи каталога, найденное по раздаче модулем рейтингов.
func (a *App) filmOfHash(ctx context.Context, hash string) int {
	var kp int
	err := a.DB.R.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT kp_id FROM lib_units WHERE (key = ?1 OR 'lib-' || id = ?1) AND kp_id > 0 LIMIT 1),
		(SELECT kinopoisk_id FROM releases WHERE infohash = ?1 AND kinopoisk_id > 0 LIMIT 1),
		(SELECT k.kp_id FROM releases r JOIN kp_releases k ON k.release_id = r.tracker || ':' || r.topic_id
			WHERE r.infohash = ?1 AND k.kp_id IS NOT NULL LIMIT 1), 0)`, strings.ToLower(hash)).Scan(&kp)
	if err != nil {
		return 0
	}
	return kp
}

// recsWanted — «Смотреть позже» человека (на всех его устройствах) номерами Кинопоиска.
func (a *App) recsWanted(ctx context.Context, owner string) []int {
	devices := `SELECT device FROM family_devices WHERE 'p:' || profile_id = ?1`
	if strings.HasPrefix(owner, "d:") {
		devices = `SELECT substr(?1, 3)`
	}
	rows, err := a.DB.R.QueryContext(ctx, `SELECT DISTINCT COALESCE(NULLIF(r.kinopoisk_id, 0), k.kp_id, 0) FROM watchlist w
		JOIN releases r ON r.id = w.release_id
		LEFT JOIN kp_releases k ON k.release_id = r.tracker || ':' || r.topic_id
		WHERE w.device IN (`+devices+`)`, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var kp int
		if rows.Scan(&kp) == nil && kp > 0 {
			out = append(out, kp)
		}
	}
	return out
}

// resolvingJournal дописывает фильмы записям журнала, опознанным позже первого просмотра, перед чтением.
type resolvingJournal struct{ h *history.Service }

func (j resolvingJournal) Views(ctx context.Context, owner string) ([]history.View, error) {
	if err := j.h.ResolveFilms(ctx); err != nil {
		return nil, err
	}
	return j.h.Views(ctx, owner)
}
