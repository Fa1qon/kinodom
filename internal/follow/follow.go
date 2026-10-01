// Package follow — подписка на новые серии (спека 11b, раздел 6): «Следить» на раздаче сериала, проверка
// страницы раздачи раз в 6 часов, оповещение «Новые серии» (общее для семьи) и докачка только новых серий;
// скачанное переходит на обновлённую версию раздачи (torrents.Upgrade).
package follow

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/catalog"
	"kinodom/internal/meta"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/internal/torrents"
)

// Сроки (спека 11b, 6.2): проверка раз в 6 часов, первая — через 10 минут после старта; переход, который
// отложили (раздачу смотрят), — снова через 10 минут; метаинфо от пиров ждём до 3 минут.
const (
	checkEvery = 6 * time.Hour
	firstCheck = 10 * time.Minute
	busyRetry  = 10 * time.Minute
	infoWait   = 3 * time.Minute
)

// ErrNotSeries — раздача не сериал: следить не за чем.
var ErrNotSeries = errors.New("это не сериал — следить не за чем")

// Catalog — каталог раздач (*catalog.Catalog).
type Catalog interface {
	CheckRelease(ctx context.Context, id int64) (catalog.Version, error)
	Release(ctx context.Context, id int64) (catalog.Release, error)
}

// Torrents — торрент-движок (*torrents.Service).
type Torrents interface {
	FetchInfo(ctx context.Context, magnet string) ([]byte, error)
	Status(ih metainfo.Hash) (torrents.TorrentStatus, bool)
	Open(ctx context.Context, src torrents.Source) (metainfo.Hash, error)
	Download(ctx context.Context, ih metainfo.Hash, files []int) error
	Upgrade(ctx context.Context, old metainfo.Hash, newRaw []byte, download []int, rekey torrents.Rekey) (metainfo.Hash, error)
}

// Watched — история просмотров (*history.Service): просмотрен ли файл на любом устройстве.
type Watched interface {
	WatchedAny(ctx context.Context, hash string, index int) (bool, error)
}

type Options struct {
	DB       *store.DB
	Catalog  Catalog
	Torrents Torrents
	History  Watched
	Rekey    torrents.Rekey                                    // ключи других модулей при переходе
	Types    func(ctx context.Context, kp int) (string, error) // вид фильма Кинопоиска по номеру; nil — по названию
	Every    time.Duration                                     // 0 — 6 часов (проверка вживую — короче)
	Log      *slog.Logger
	Now      func() time.Time
}

// Module — модуль «follow» под сторожем.
type Module struct {
	o     Options
	st    followDB
	log   *slog.Logger
	now   func() time.Time
	every time.Duration
	wake  chan struct{}

	mu      sync.Mutex
	retryAt map[int64]time.Time // раздача → когда повторить отложенный переход
}

func New(o Options) *Module {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	every := o.Every
	if every <= 0 {
		every = checkEvery
	}
	return &Module{o: o, st: followDB{o.DB}, log: o.Log, now: o.Now, every: every, wake: make(chan struct{}, 1), retryAt: map[int64]time.Time{}}
}

func (m *Module) Name() string { return "follow" }

// Run — первая проверка через 10 минут после старта, дальше раздачи проверяются, когда подошёл их срок
// (заглядывает раз в 10 минут: отложенные переходы и свежие подписки без списка серий).
func (m *Module) Run(ctx context.Context) error {
	supervisor.Ready(ctx)
	tick := min(m.every, busyRetry)
	timer := time.NewTimer(min(firstCheck, m.every))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
			m.pass(ctx)
		case <-timer.C:
			m.pass(ctx)
			timer.Reset(tick)
		}
	}
}

var seriesTypes = map[string]bool{"TV_SERIES": true, "MINI_SERIES": true, "TV_SHOW": true}

// series — раздача сериала (спека 11b, 6.1): вид с Кинопоиска, если номер известен; иначе — в названии
// сезон или серии. Фильм с дополнительными файлами сериалом не считается.
func (m *Module) series(ctx context.Context, rel catalog.Release) bool {
	if kp := rel.Rating.KinopoiskID; kp > 0 && m.o.Types != nil {
		if typ, err := m.o.Types(ctx, kp); err == nil && typ != "" {
			return seriesTypes[typ]
		}
	}
	return meta.ParseTitle(rel.Title).Series
}

// Series — раздача сериала: «Следить» показывается.
func (m *Module) Series(ctx context.Context, rel catalog.Release) bool { return m.series(ctx, rel) }

// Follow — «Следить»: запомнить нынешнюю версию раздачи и её серии. Списка серий ещё нет (Rutracker без
// .torrent) — он придёт от пиров первой проверкой, сразу.
func (m *Module) Follow(ctx context.Context, release int64) error {
	rel, err := m.o.Catalog.Release(ctx, release)
	if err != nil {
		return err
	}
	if !m.series(ctx, rel) {
		return ErrNotSeries
	}
	var paths []string
	if len(rel.Torrent) > 0 {
		files, err := torrents.PlayableFiles(rel.Torrent)
		if err == nil {
			for _, f := range files {
				paths = append(paths, f.Name)
			}
		}
	}
	_, to, total := meta.Episodes(rel.Title)
	now := m.now()
	if err := m.st.follow(ctx, Follow{Release: release, InfoHash: rel.InfoHash, Episodes: max(len(paths), to), Total: total, Paths: paths}, now); err != nil {
		return err
	}
	if err := m.st.setChecked(ctx, release, now); err != nil {
		return err
	}
	if len(paths) == 0 {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Unfollow — «Не следить».
func (m *Module) Unfollow(ctx context.Context, release int64) error {
	m.mu.Lock()
	delete(m.retryAt, release)
	m.mu.Unlock()
	return m.st.unfollow(ctx, release)
}

// State — подписка на раздачу: active, finished, removed; "" — не следят.
func (m *Module) State(ctx context.Context, release int64) (string, error) {
	f, ok, err := m.st.get(ctx, release)
	if err != nil || !ok {
		return "", err
	}
	return f.State, nil
}

// UpdateView — строка «Новых серий» для пульта.
type UpdateView struct {
	ID       int64        `json:"id"`
	Release  int64        `json:"releaseId"`
	Kind     string       `json:"kind"` // episodes, removed
	Title    string       `json:"title"`
	ImageKey string       `json:"imageKey"`
	Label    string       `json:"label"` // «1×07–1×08»
	Hash     string       `json:"hash"`  // версия раздачи с новыми сериями
	Files    []UpdateFile `json:"files"` // «Смотреть» — первая
	At       time.Time    `json:"at"`
}

// Updates — непросмотренные оповещения, новые сверху. Строка, все серии которой досмотрели на любом
// устройстве, убирается сама (спека 11b, 6.1).
func (m *Module) Updates(ctx context.Context) ([]UpdateView, error) {
	us, err := m.st.updates(ctx)
	if err != nil {
		return nil, err
	}
	out := []UpdateView{}
	for _, u := range us {
		if u.Kind == KindEpisodes && len(u.Files) > 0 {
			seen := true
			for _, f := range u.Files {
				w, err := m.o.History.WatchedAny(ctx, u.InfoHash, f.Index)
				if err != nil {
					return nil, err
				}
				seen = seen && w
			}
			if seen {
				if err := m.st.dismiss(ctx, u.ID); err != nil {
					return nil, err
				}
				continue
			}
		}
		v := UpdateView{ID: u.ID, Release: u.Release, Kind: u.Kind, Label: u.Label, Hash: u.InfoHash, Files: nonNil(u.Files), At: u.At}
		if rel, err := m.o.Catalog.Release(ctx, u.Release); err == nil {
			v.Title, v.ImageKey = rel.Title, rel.ImageKey
		}
		out = append(out, v)
	}
	return out, nil
}

// Unread — сколько строк в «Новых сериях»: число у колокольчика.
func (m *Module) Unread(ctx context.Context) (int, error) {
	us, err := m.Updates(ctx)
	return len(us), err
}

// Dismiss — «Убрать».
func (m *Module) Dismiss(ctx context.Context, id int64) error { return m.st.dismiss(ctx, id) }
