package iptv

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
)

// ErrNoChannel — такого канала нет (ни в составе, ни в телепрограмме).
var ErrNoChannel = errors.New("такого канала нет")

// ErrNoStream — такого источника нет.
var ErrNoStream = errors.New("такого источника нет")

// FieldError — неверное значение в правке: текст для человека.
type FieldError struct{ Text string }

func (e *FieldError) Error() string { return e.Text }

var (
	reCountry  = regexp.MustCompile(`^[A-Z]{2}$`)
	reLanguage = regexp.MustCompile(`^[a-z]{3}$`)
)

// CheckCategory, CheckCountry, CheckLanguage — значения меток из пульта; "" — «не указана».
func CheckCategory(c string) error {
	if !slices.Contains(labels.CategoryOrder, c) {
		return &FieldError{"неизвестная категория " + c}
	}
	return nil
}

func CheckCountry(c string) error {
	if c != "" && !reCountry.MatchString(c) {
		return &FieldError{"страна — код из двух заглавных латинских букв, например RU"}
	}
	return nil
}

func CheckLanguage(l string) error {
	if l != "" && !reLanguage.MatchString(l) {
		return &FieldError{"язык — код из трёх строчных латинских букв, например rus"}
	}
	return nil
}

// knownKey — ключ существующего канала: в составе или в телепрограмме (в том числе «id+N»).
func (m *Module) knownKey(key string) bool {
	if _, ok := m.Lineup().ByKey[key]; ok {
		return true
	}
	id, _ := parseKey(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epg.has(id)
}

// Override — правки канала (копия).
func (m *Module) Override(key string) Override {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pool.overrides[key]
}

// SetOverride — правки канала: скрыть, метки, закреплённый источник.
func (m *Module) SetOverride(ctx context.Context, key string, o Override) error {
	if !m.knownKey(key) {
		return ErrNoChannel
	}
	if err := m.d.setOverride(ctx, key, o); err != nil {
		return err
	}
	m.mu.Lock()
	if o.empty() {
		delete(m.pool.overrides, key)
	} else {
		m.pool.overrides[key] = o
	}
	m.mu.Unlock()
	m.rebuild(ctx)
	return nil
}

// Favorites — избранное устройства.
func (m *Module) Favorites(ctx context.Context, device string) ([]string, error) {
	return m.d.favorites(ctx, device)
}

// SetFavorites — избранное устройства целиком, по порядку.
func (m *Module) SetFavorites(ctx context.Context, device string, keys []string) error {
	if device == "" {
		return &FieldError{"устройство не определилось"}
	}
	return m.d.setFavorites(ctx, device, keys)
}

// SetNameRule — назначение из «Не распознано»: нормализованное название → канал; hidden — скрыть;
// channel == "" и !hidden — снять правку.
func (m *Module) SetNameRule(ctx context.Context, name, channel string, hidden bool) error {
	name = m3u.Norm(name)
	if name == "" {
		return &FieldError{"пустое название"}
	}
	var r *Rule
	switch {
	case hidden:
		r = &Rule{Hidden: true}
	case channel != "":
		if !m.knownKey(channel) {
			return ErrNoChannel
		}
		r = &Rule{Channel: channel}
	}
	if err := m.d.setNameRule(ctx, name, r); err != nil {
		return err
	}
	m.mu.Lock()
	if r == nil {
		delete(m.pool.nameRules, name)
	} else {
		m.pool.nameRules[name] = *r
	}
	m.mu.Unlock()
	m.rebuild(ctx)
	return nil
}

// SetStreamRule — «это другой канал» для одного источника (по ссылке).
func (m *Module) SetStreamRule(ctx context.Context, id int64, channel string, hidden bool) error {
	m.mu.Lock()
	s := m.pool.streams[id]
	url := ""
	if s != nil {
		url = s.URL
	}
	m.mu.Unlock()
	if s == nil {
		return ErrNoStream
	}
	var r *Rule
	switch {
	case hidden:
		r = &Rule{Hidden: true}
	case channel != "":
		if !m.knownKey(channel) {
			return ErrNoChannel
		}
		r = &Rule{Channel: channel}
	}
	if err := m.d.setStreamRule(ctx, url, r); err != nil {
		return err
	}
	m.mu.Lock()
	if r == nil {
		delete(m.pool.streamRules, url)
	} else {
		m.pool.streamRules[url] = *r
	}
	m.mu.Unlock()
	m.rebuild(ctx)
	return nil
}

// StreamURL — ссылка источника по id; "" — нет.
func (m *Module) StreamURL(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.pool.streams[id]; s != nil {
		return s.URL
	}
	return ""
}

// EPGChannel — канал телепрограммы для поиска при назначении.
type EPGChannel struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// SearchEPG — каналы телепрограммы, в названиях которых есть q (без учёта регистра и меток качества):
// сначала точные совпадения, потом начинающиеся с q, потом остальные; не больше limit.
func (m *Module) SearchEPG(q string, limit int) []EPGChannel {
	q = m3u.Norm(q)
	m.mu.Lock()
	g := m.guide
	m.mu.Unlock()
	if g == nil || q == "" {
		return []EPGChannel{}
	}
	type hit struct {
		c    EPGChannel
		rank int
	}
	var hits []hit
	for _, c := range g.Channels {
		best := -1
		for _, n := range c.Names {
			nn := m3u.Norm(n)
			switch {
			case nn == q:
				best = 0
			case strings.HasPrefix(nn, q) && (best < 0 || best > 1):
				best = 1
			case strings.Contains(nn, q) && best < 0:
				best = 2
			}
		}
		if best >= 0 && len(c.Names) > 0 {
			hits = append(hits, hit{EPGChannel{Key: c.ID, Name: c.Names[0]}, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].c.Name < hits[j].c.Name
	})
	out := []EPGChannel{}
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.c)
	}
	return out
}

// LogoURL — адрес логотипа канала в интернете; "" — нет.
func (m *Module) LogoURL(key string) string {
	if c := m.Lineup().ByKey[key]; c != nil {
		return c.Logo
	}
	return ""
}

// Location — часовой пояс каналов.
func (m *Module) Location() *time.Location {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loc
}

// Weeks — «днём / вечером» по источникам за неделю.
func (m *Module) Weeks(ctx context.Context, ids []int64) (map[int64]Week, error) {
	return m.d.weeks(ctx, ids, m.now().Add(-keepChecks), m.Location())
}

// PlaylistView — плейлист для пульта.
type PlaylistView struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	URL         string     `json:"url"` // "" — файл
	Limited     bool       `json:"limited"`
	AddedAt     time.Time  `json:"addedAt"`
	UpdatedAt   *time.Time `json:"updatedAt"`
	Error       string     `json:"error"`
	Entries     int        `json:"entries"`
	Unsupported int        `json:"unsupported"`
	Recognized  int        `json:"recognized"`
	Alive       int        `json:"alive"`
}

// SourceState — телепрограмма или база iptv-org для пульта.
type SourceState struct {
	URL       string     `json:"url,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt"`
	Error     string     `json:"error"`
	Channels  int        `json:"channels,omitempty"`
}

// PlaylistsView — «Настройки → Каналы»: плейлисты, телепрограмма, база, ход проверок.
type PlaylistsView struct {
	Items   []PlaylistView `json:"items"`
	EPG     SourceState    `json:"epg"`
	IPTVOrg SourceState    `json:"iptvorg"`
	Light   Progress       `json:"light"`
	Full    Progress       `json:"full"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Playlists — плейлисты с числами.
func (m *Module) Playlists() PlaylistsView {
	l := m.Lineup()
	m.mu.Lock()
	defer m.mu.Unlock()
	type counts struct{ entries, recognized, alive int }
	c := map[int64]*counts{}
	for _, s := range m.pool.streams {
		seen := map[int64]bool{}
		for _, e := range s.Entries {
			n := c[e.Playlist]
			if n == nil {
				n = &counts{}
				c[e.Playlist] = n
			}
			n.entries++
			if seen[e.Playlist] {
				continue
			}
			seen[e.Playlist] = true
			if _, ok := l.StreamChannel[s.ID]; ok {
				n.recognized++
			}
			if s.State == StateAlive {
				n.alive++
			}
		}
	}
	out := PlaylistsView{Items: []PlaylistView{}, Light: m.light, Full: m.full,
		EPG:     SourceState{URL: m.epgURL, UpdatedAt: timePtr(m.epgSrc.at), Error: m.epgSrc.err},
		IPTVOrg: SourceState{UpdatedAt: timePtr(m.orgSrc.at), Error: m.orgSrc.err}}
	if m.guide != nil {
		out.EPG.Channels = len(m.guide.Channels)
	}
	for _, pl := range m.pool.playlists {
		v := PlaylistView{ID: pl.ID, Name: pl.Name, URL: pl.URL, Limited: pl.Limited, AddedAt: pl.AddedAt,
			UpdatedAt: timePtr(pl.UpdatedAt), Error: pl.Error, Unsupported: pl.Unsupported}
		if n := c[pl.ID]; n != nil {
			v.Entries, v.Recognized, v.Alive = n.entries, n.recognized, n.alive
		}
		out.Items = append(out.Items, v)
	}
	slices.SortFunc(out.Items, func(a, b PlaylistView) int { return a.AddedAt.Compare(b.AddedAt) })
	return out
}

// Status — поля модуля для «Состояния».
func (m *Module) Status() map[string]any {
	l := m.Lineup()
	m.mu.Lock()
	defer m.mu.Unlock()
	alive, visible := 0, 0
	for _, s := range m.pool.streams {
		if s.State == StateAlive {
			alive++
		}
	}
	for _, c := range l.Order {
		if c.Hidden == "" {
			visible++
		}
	}
	return map[string]any{
		"playlists": len(m.pool.playlists), "streams": len(m.pool.streams), "alive": alive,
		"channels": len(l.Order), "visible": visible,
		"epgUpdatedAt": timePtr(m.epgSrc.at), "iptvorgUpdatedAt": timePtr(m.orgSrc.at),
		"light": m.light, "full": m.full,
	}
}
