// Package iptv — модуль «iptv» (спека этапа 8): плейлисты, которые «скармливают» серверу, пул
// источников, сопоставление с каналами телепрограммы, метки iptv-org, проверки и состав каналов для
// пульта и телевизоров.
package iptv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/probe"
	"kinodom/internal/iptv/xmltv"
	"kinodom/internal/netx"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// Источники по умолчанию (спека этапа 8, разделы 5.4 и 5.7). Переменные: тесты (offlinetest)
// подменяют их, чтобы не ходить в интернет.
var (
	DefaultEPGURL  = "https://iptvx.one/epg/epg_lite.xml.gz"
	DefaultOrgBase = "https://iptv-org.github.io/api"
)

// Расписание и пределы (спека этапа 8, разделы 5.1, 5.7, 5.8).
const (
	playlistEvery  = 24 * time.Hour
	epgEvery       = 24 * time.Hour
	orgEvery       = 7 * 24 * time.Hour
	staleAfter     = 48 * time.Hour      // плейлист или телепрограмма не обновляются — проблема
	orgStaleAfter  = 14 * 24 * time.Hour // база iptv-org
	keepChecks     = 7 * 24 * time.Hour
	maxPlaylist    = 32 << 20
	maxEPG         = 256 << 20
	maxOrg         = 64 << 20
	lightParallel  = 32
	fullParallel   = 8
	fullPerChannel = 3
	lightHour      = 4
	rebuildDelay   = 2 * time.Second
	shareEvery     = 10 * time.Minute
)

// fullHours — часы полной проверки: раз в 3 часа и каждый час вечером (спека, раздел 5.8).
var fullHours = []int{0, 3, 6, 9, 12, 15, 18, 19, 20, 21, 22}

// retryDelays — после неудачного скачивания: 1, 5, 15 минут, дальше каждые 15 (как у каталога).
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

func retryAfter(fails int) time.Duration { return retryDelays[min(fails, len(retryDelays))-1] }

type Options struct {
	DB      *store.DB
	Dir     string       // data\iptv: телепрограмма и база iptv-org
	Client  *http.Client // напрямую; nil — транспорт без прокси
	EPGURL  string       // "" — DefaultEPGURL
	OrgBase string       // "" — DefaultOrgBase
	Hidden  Hidden
	// Location — часовой пояс каналов: версия федеральных каналов, расписание проверок, «вечер», окно
	// телепрограммы. nil — UTC+7 (спека этапа 8, раздел 5.5: Windows на ПК заказчика — московское время).
	Location *time.Location
	Log      *slog.Logger
	Now      func() time.Time // тесты
	Prober   *probe.Prober    // тесты; nil — по спеке
}

// Progress — ход прохода проверок для пульта.
type Progress struct {
	Running  bool      `json:"running"`
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Finished time.Time `json:"finished,omitzero"` // последний закончившийся проход
}

// source — телепрограмма или база iptv-org: когда скачали, ошибки, когда пробовать снова.
type source struct {
	at    time.Time // последнее удачное скачивание (время файла)
	next  time.Time
	fails int
	err   string
}

// Module — модуль «iptv».
type Module struct {
	o      Options
	d      db
	log    *slog.Logger
	now    func() time.Time // без пояса; местное время — m.local()
	client *http.Client
	prober *probe.Prober

	dirty    chan struct{} // пересобрать состав
	wake     chan struct{} // проверить, не пора ли что-то скачать
	lightNew chan struct{} // проверить новые источники

	mu       sync.Mutex
	loc      *time.Location
	runCtx   context.Context
	pool     *pool
	guide    *xmltv.Guide
	guideDay string // день окна телепрограммы, «2026-09-29»
	epg      *epgIndex
	base     *labels.Base
	lineup   *Lineup
	hidden   Hidden
	epgURL   string
	share    map[int64]float64
	shareAt  time.Time
	epgSrc   source
	orgSrc   source
	plFails  map[int64]int       // неудачных обновлений плейлиста подряд
	plNext   map[int64]time.Time // когда обновлять плейлист по ссылке
	light    Progress
	full     Progress
}

func New(o Options) *Module {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = DefaultLocation
	}
	if o.EPGURL == "" {
		o.EPGURL = DefaultEPGURL
	}
	if o.OrgBase == "" {
		o.OrgBase = DefaultOrgBase
	}
	m := &Module{o: o, d: db{o.DB}, log: o.Log, now: o.Now, loc: o.Location, client: o.Client, prober: o.Prober,
		dirty: make(chan struct{}, 1), wake: make(chan struct{}, 1), lightNew: make(chan struct{}, 1),
		pool: newPool(), epg: newEPGIndex(nil), hidden: o.Hidden, epgURL: o.EPGURL,
		plFails: map[int64]int{}, plNext: map[int64]time.Time{}}
	if m.client == nil {
		m.client = &http.Client{Transport: netx.NewTransport(nil)}
	}
	if m.prober == nil {
		m.prober = &probe.Prober{Client: m.client}
	}
	m.lineup = &Lineup{ByKey: map[string]*Channel{}, StreamChannel: map[int64]string{}}
	return m
}

func (m *Module) Name() string { return "iptv" }

// Run — пул из базы, телепрограмма и база iptv-org с диска, затем фоновые работы: обновления,
// пересборка состава, проверки.
func (m *Module) Run(ctx context.Context) error {
	p, err := m.d.load(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.pool, m.runCtx = p, ctx
	m.mu.Unlock()
	m.rebuild(ctx)
	supervisor.Ready(ctx)
	supervisor.Go(ctx, func(ctx context.Context) error {
		m.loadFiles(ctx)
		m.poke(m.wake)
		m.poke(m.lightNew)
		return nil
	})
	supervisor.Go(ctx, m.rebuildLoop)
	supervisor.Go(ctx, m.updateLoop)
	supervisor.Go(ctx, m.lightLoop)
	supervisor.Go(ctx, m.fullLoop)
	<-ctx.Done()
	return nil
}

func (m *Module) poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// changed — состав нужно пересобрать (не чаще раза в rebuildDelay).
func (m *Module) changed() { m.poke(m.dirty) }

func (m *Module) rebuildLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.dirty:
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rebuildDelay):
		}
		m.rebuild(ctx)
	}
}

// DefaultLocation — часовой пояс каналов по умолчанию: UTC+7, как у заказчика.
var DefaultLocation = Zone(7)

// Zone — часовой пояс UTC+offset.
func Zone(offset int) *time.Location {
	return time.FixedZone(fmt.Sprintf("UTC%+d", offset), offset*3600)
}

// local — сейчас в часовом поясе каналов.
func (m *Module) local() time.Time {
	m.mu.Lock()
	loc := m.loc
	m.mu.Unlock()
	return m.now().In(loc)
}

// SetLocation — часовой пояс каналов поменяли в пульте.
func (m *Module) SetLocation(loc *time.Location) {
	m.mu.Lock()
	m.loc = loc
	m.mu.Unlock()
	m.poke(m.wake) // окно телепрограммы — по новому поясу
	m.rebuild(context.Background())
}

// localShift — местный сдвиг от Москвы в часах: UTC+7 — 4.
func localShift(t time.Time) int {
	_, off := t.Zone()
	return off/3600 - 3
}

// rebuild — собрать состав сейчас.
func (m *Module) rebuild(ctx context.Context) {
	now := m.local()
	m.mu.Lock()
	needShare := m.share == nil || now.Sub(m.shareAt) > shareEvery
	m.mu.Unlock()
	if needShare {
		share, err := m.d.goodShare(ctx, now.Add(-keepChecks))
		if err != nil {
			m.log.Warn("iptv: доля хороших проверок не читается", "err", err)
		}
		m.mu.Lock()
		if err == nil {
			m.share, m.shareAt = share, now
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lineup = build(buildInput{pool: m.pool, epg: m.epg, base: m.base, hidden: m.hidden, localShift: localShift(now),
		goodShare: m.share, now: now})
}

// Lineup — последний собранный состав.
func (m *Module) Lineup() *Lineup {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lineup
}

// Guide — телепрограмма; nil — ещё не загружена.
func (m *Module) Guide() *xmltv.Guide {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.guide
}

// SetHidden — настройки скрытия поменяли в пульте.
func (m *Module) SetHidden(h Hidden) {
	m.mu.Lock()
	m.hidden = h
	m.mu.Unlock()
	m.rebuild(context.Background())
}

// SetEPGURL — ссылку телепрограммы поменяли: скачать сразу.
func (m *Module) SetEPGURL(u string) {
	if u == "" {
		u = DefaultEPGURL
	}
	m.mu.Lock()
	m.epgURL = u
	m.epgSrc.next, m.epgSrc.fails = time.Time{}, 0
	m.mu.Unlock()
	m.poke(m.wake)
}

func (m *Module) epgPath() string { return filepath.Join(m.o.Dir, "epg.xml.gz") }
func (m *Module) orgPaths() (string, string) {
	return filepath.Join(m.o.Dir, "iptvorg-channels.json"), filepath.Join(m.o.Dir, "iptvorg-feeds.json")
}

// window — окно передач: от 12 часов до начала сегодняшнего дня до конца послезавтрашнего (спека,
// раздел 5.7).
func window(now time.Time) (time.Time, time.Time, string) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return day.Add(-12 * time.Hour), day.AddDate(0, 0, 3), day.Format(time.DateOnly)
}

// loadFiles — телепрограмма и база iptv-org с диска (скачанные раньше).
func (m *Module) loadFiles(ctx context.Context) {
	if fi, err := os.Stat(m.epgPath()); err == nil {
		if err := m.loadGuide(m.epgPath()); err != nil {
			m.log.Warn("iptv: сохранённая телепрограмма не читается", "err", err)
		} else {
			m.mu.Lock()
			m.epgSrc.at = fi.ModTime()
			m.epgSrc.next = fi.ModTime().Add(epgEvery)
			m.mu.Unlock()
		}
	}
	cp, fp := m.orgPaths()
	if fi, err := os.Stat(cp); err == nil {
		if err := m.loadOrg(cp, fp); err != nil {
			m.log.Warn("iptv: сохранённая база iptv-org не читается", "err", err)
		} else {
			m.mu.Lock()
			m.orgSrc.at = fi.ModTime()
			m.orgSrc.next = fi.ModTime().Add(orgEvery)
			m.mu.Unlock()
		}
	}
	m.rebuild(ctx)
}

func (m *Module) loadGuide(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	from, to, day := window(m.local())
	g, err := xmltv.Parse(f, from, to)
	if err != nil {
		return err
	}
	ix := newEPGIndex(g)
	m.mu.Lock()
	m.guide, m.epg, m.guideDay = g, ix, day
	m.mu.Unlock()
	return nil
}

func (m *Module) loadOrg(channels, feeds string) error {
	c, err := os.Open(channels)
	if err != nil {
		return err
	}
	defer c.Close()
	f, err := os.Open(feeds)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := labels.LoadBase(c, f)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.base = b
	m.mu.Unlock()
	return nil
}
