package iptv

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/xmltv"
)

// Hidden — настройки скрытия (спека этапа 8, раздел 5.6).
type Hidden struct {
	Categories []string
	Countries  []string
	Languages  []string
	OtherZones bool
}

// Причины, по которым канал скрыт.
const (
	HiddenChannel  = "channel"
	HiddenCategory = "category"
	HiddenCountry  = "country"
	HiddenLanguage = "language"
	HiddenZone     = "zone"
)

// Channel — канал в составе.
type Channel struct {
	Key     string
	EPGID   string // канал телепрограммы; у сдвинутого — базовый
	Shift   int    // сдвиг программы в часах у сдвинутого канала <id>+<N>; 0 — своя программа
	Zone    int    // часовой пояс версии: N у «-plN» и «+N», 0 — московская
	Name    string
	Logo    string // адрес логотипа в интернете; "" — нет
	Labels  labels.Labels
	Federal int       // номер кнопки 1–20; 0 — не федеральный
	Sources []*Stream // предлагаемые источники по порядку
	Others  []*Stream // остальные привязанные: молчат, мертвы, новые
	// Rejected — скрытые у канала вручную («Скрыть» в настройках канала): не предлагаются и полной
	// проверкой в фоне не проверяются (лёгкой — да: они запасные). Других предлагаемых нет — живые
	// скрытые переходят в Sources запасными, чтобы канал не пропал вместе с кнопкой «Вернуть».
	Rejected []*Stream
	Grade    string // оценка первого источника: green, yellow, red, unrated; "" — источников нет
	Hidden   string // почему скрыт (без учёта избранного); "" — на экране
	Pinned   string // закреплённая ссылка
}

// Offered — у канала есть что предложить плееру.
func (c *Channel) Offered() bool { return len(c.Sources) > 0 }

// Group — нераспознанное название: потоки, которые ни к какому каналу не привязались.
type Group struct {
	Name      string  // нормализованное
	Sample    string  // исходное название одной из записей
	Streams   []int64 // id источников
	Alive     int
	Playlists []int64
}

// Lineup — состав каналов: снимок, который читает API.
type Lineup struct {
	Order         []*Channel          // федеральные по номеру, потом остальные — по категориям; только с источниками
	ByKey         map[string]*Channel // все каналы, к которым привязан хоть один поток
	Unrecognized  []Group             // по убыванию живых, потом потоков
	StreamChannel map[int64]string    // источник → ключ канала (только привязанные)
	LocalShift    int
	Built         time.Time
}

// epgIndex — справочник каналов телепрограммы для сопоставления.
type epgIndex struct {
	guide  *xmltv.Guide
	byNorm map[string][]string
}

func newEPGIndex(g *xmltv.Guide) *epgIndex {
	ix := &epgIndex{guide: g, byNorm: map[string][]string{}}
	if g == nil {
		return ix
	}
	for _, c := range g.Channels {
		seen := map[string]bool{}
		for _, n := range c.Names {
			k := m3u.Norm(n)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			ix.byNorm[k] = append(ix.byNorm[k], c.ID)
		}
	}
	return ix
}

func (ix *epgIndex) has(id string) bool {
	if ix.guide == nil {
		return false
	}
	_, ok := ix.guide.Channel(id)
	return ok
}

// byName — единственный канал с таким нормализованным названием; "" и признак двусмысленности.
func (ix *epgIndex) byName(norm string) (string, bool) {
	ids := ix.byNorm[norm]
	switch len(ids) {
	case 0:
		return "", false
	case 1:
		return ids[0], false
	}
	return "", true
}

var reZone = regexp.MustCompile(`-pl([1-9])$`)

// zoneOf — часовой пояс версии по id телепрограммы: «pervy-pl4» — 4.
func zoneOf(id string) int {
	if m := reZone.FindStringSubmatch(id); m != nil {
		return int(m[1][0] - '0')
	}
	return 0
}

// parseKey — ключ канала: «spas+4» — базовый id и сдвиг программы; «pervy-pl4» — сам id, 0.
func parseKey(key string) (string, int) {
	if i := strings.LastIndexByte(key, '+'); i > 0 {
		if n, err := strconv.Atoi(key[i+1:]); err == nil && n >= 1 && n <= 9 {
			return key[:i], n
		}
	}
	return key, 0
}

// regional — версия канала id для сдвига n: своя в телепрограмме («-plN») или сдвинутая «id+N».
func (ix *epgIndex) regional(id string, n int) string {
	if n <= 0 || zoneOf(id) != 0 {
		return id
	}
	if own := id + "-pl" + strconv.Itoa(n); ix.has(own) {
		return own
	}
	return id + "+" + strconv.Itoa(n)
}

// entryShift — сдвиг записи: tvg-shift или «+N» в названии.
func entryShift(e Entry) int {
	if e.Shift >= 1 && e.Shift <= 9 {
		return e.Shift
	}
	_, n := m3u.SplitShift(m3u.Norm(e.Name))
	return n
}

// matchResult — к чему привязался поток.
type matchResult struct {
	key    string // ключ канала; "" — не привязан
	hidden bool   // скрыт правкой
	by     string // rule, name-rule, tvg-id, bridge, name
}

// match — очередь признаков (спека этапа 8, раздел 5.3): правка по ссылке, по названию, tvg-id,
// мост iptv-org, название.
func match(s *Stream, p *pool, ix *epgIndex, base *labels.Base) matchResult {
	if r, ok := p.streamRules[s.URL]; ok {
		return matchResult{key: r.Channel, hidden: r.Hidden, by: "rule"}
	}
	for _, e := range s.Entries {
		if r, ok := p.nameRules[m3u.Norm(e.Name)]; ok {
			return matchResult{key: r.Channel, hidden: r.Hidden, by: "name-rule"}
		}
	}
	for _, e := range s.Entries {
		if e.Shift < 0 { // отрицательный сдвиг (Калининград) не распознаётся — в «Не распознано» (спека, 5.3)
			continue
		}
		if e.TvgID != "" && ix.has(e.TvgID) {
			return matchResult{key: ix.regional(e.TvgID, entryShift(e)), by: "tvg-id"}
		}
	}
	for _, e := range s.Entries {
		oc := base.ByID(orgID(e.TvgID))
		if oc == nil || e.Shift < 0 {
			continue
		}
		found, ambiguous := "", false
		for _, n := range oc.Names() {
			id, amb := ix.byName(m3u.Norm(n))
			switch {
			case amb:
				ambiguous = true
			case id == "":
			case found == "":
				found = id
			case found != id:
				ambiguous = true
			}
		}
		if found != "" && !ambiguous {
			return matchResult{key: ix.regional(found, entryShift(e)), by: "bridge"}
		}
	}
	// Название; последней попыткой — без города в скобках («Россия 24 +0 (Липецк)»).
	for _, strip := range []bool{false, true} {
		for _, e := range s.Entries {
			if e.Shift < 0 {
				continue
			}
			for _, name := range []string{e.Name, e.TvgName} {
				if strip {
					if name = m3u.WithoutPlace(name); name == e.Name || name == e.TvgName {
						continue
					}
				}
				if key := ix.byNameShift(m3u.Norm(name)); key != "" {
					return matchResult{key: key, by: "name"}
				}
			}
		}
	}
	return matchResult{}
}

// byNameShift — канал по нормализованному названию; «+N» без своего канала — сдвинутый «id+N».
func (ix *epgIndex) byNameShift(n string) string {
	if n == "" {
		return ""
	}
	if id, amb := ix.byName(n); id != "" {
		return id
	} else if amb {
		return ""
	}
	if b, shift := m3u.SplitShift(n); shift > 0 {
		if id, _ := ix.byName(b); id != "" {
			return ix.regional(id, shift)
		}
	}
	return ""
}

// orgID — id канала iptv-org из tvg-id вида «Channel.ru@HD».
func orgID(tvgID string) string {
	id, _, _ := strings.Cut(tvgID, "@")
	return id
}

// buildInput — всё, из чего собирается состав.
type buildInput struct {
	pool       *pool
	epg        *epgIndex
	base       *labels.Base
	hidden     Hidden
	localShift int
	goodShare  map[int64]float64
	now        time.Time
}

// build — состав каналов (спека этапа 8, разделы 5.3–5.6 и 5.9).
func build(in buildInput) *Lineup {
	l := &Lineup{ByKey: map[string]*Channel{}, StreamChannel: map[int64]string{}, LocalShift: in.localShift, Built: in.now}
	p := in.pool
	groups := map[string]*Group{}
	streams := make([]*Stream, 0, len(p.streams))
	for _, s := range p.streams {
		streams = append(streams, s)
	}
	slices.SortFunc(streams, func(a, b *Stream) int { return cmp.Compare(a.ID, b.ID) })
	limited := map[int64]bool{}
	for id, pl := range p.playlists {
		limited[id] = pl.Limited
	}
	for _, s := range streams {
		r := match(s, p, in.epg, in.base)
		switch {
		case r.hidden:
			continue
		case r.key == "":
			if len(s.Entries) == 0 {
				continue
			}
			n := m3u.Norm(s.Entries[0].Name)
			if n == "" {
				continue
			}
			g := groups[n]
			if g == nil {
				g = &Group{Name: n, Sample: s.Entries[0].Name}
				groups[n] = g
			}
			g.Streams = append(g.Streams, s.ID)
			if s.State == StateAlive {
				g.Alive++
			}
			for _, e := range s.Entries {
				if !slices.Contains(g.Playlists, e.Playlist) {
					g.Playlists = append(g.Playlists, e.Playlist)
				}
			}
			continue
		}
		l.StreamChannel[s.ID] = r.key
		c := l.ByKey[r.key]
		if c == nil {
			c = newChannel(r.key, in.epg)
			l.ByKey[r.key] = c
		}
		switch {
		case slices.Contains(p.overrides[r.key].HiddenURLs, s.URL):
			c.Rejected = append(c.Rejected, s)
		case offered(s, limited):
			c.Sources = append(c.Sources, s)
		default:
			c.Others = append(c.Others, s)
		}
	}
	for _, c := range l.ByKey {
		if len(c.Sources) == 0 {
			c.Rejected = slices.DeleteFunc(c.Rejected, func(s *Stream) bool {
				if offered(s, limited) {
					c.Sources = append(c.Sources, s)
					return true
				}
				return false
			})
		}
	}
	for _, g := range groups {
		l.Unrecognized = append(l.Unrecognized, *g)
	}
	slices.SortFunc(l.Unrecognized, func(a, b Group) int {
		return cmp.Or(cmp.Compare(b.Alive, a.Alive), cmp.Compare(len(b.Streams), len(a.Streams)), cmp.Compare(a.Name, b.Name))
	})

	// Метки: сначала каналы без сдвига — региональные версии берут метки у них.
	keys := make([]string, 0, len(l.ByKey))
	for k := range l.ByKey {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(l.ByKey[a].Zone, l.ByKey[b].Zone), cmp.Compare(a, b)) })
	resolved := map[string]labels.Labels{}
	for _, k := range keys {
		c := l.ByKey[k]
		o := p.overrides[k]
		c.Hidden, c.Pinned = "", o.PinnedURL
		c.Labels = channelLabels(c, o, in, l, resolved)
		resolved[c.EPGID] = c.Labels
		sortSources(c, in.goodShare)
	}

	// Федеральные: версия под местный пояс, если у неё есть источник, иначе московская.
	for i, id := range Federal {
		for _, k := range federalCandidates(id, in.localShift, in.epg) {
			c := l.ByKey[k]
			if c != nil && c.Offered() && !p.overrides[k].Hidden {
				c.Federal = i + 1
				break
			}
		}
	}
	var fed, rest []*Channel
	for _, k := range keys {
		c := l.ByKey[k]
		c.Hidden = hiddenBy(c, p.overrides[k], in) // и у каналов без источников: их молчащие источники проверяются
		if !c.Offered() {
			continue
		}
		if c.Federal > 0 {
			fed = append(fed, c)
		} else {
			rest = append(rest, c)
		}
	}
	slices.SortFunc(fed, func(a, b *Channel) int { return cmp.Compare(a.Federal, b.Federal) })
	slices.SortFunc(rest, func(a, b *Channel) int { return compareChannels(a, b, in.localShift) })
	l.Order = append(fed, rest...)
	return l
}

func newChannel(key string, ix *epgIndex) *Channel {
	id, shift := parseKey(key)
	c := &Channel{Key: key, EPGID: id, Shift: shift, Zone: shift}
	if c.Zone == 0 {
		c.Zone = zoneOf(id)
	}
	if ix.guide != nil {
		if ec, ok := ix.guide.Channel(id); ok {
			if len(ec.Names) > 0 {
				c.Name = ec.Names[0]
			}
			c.Logo = ec.Icon
		}
	}
	if c.Name != "" && shift > 0 {
		c.Name += " +" + strconv.Itoa(shift)
	}
	return c
}

// offered — источник можно предлагать плееру: жив; новый — только из «ограниченного» плейлиста
// (они в фоне не проверяются).
func offered(s *Stream, limited map[int64]bool) bool {
	switch s.State {
	case StateAlive:
		return true
	case StateNew:
		for _, e := range s.Entries {
			if limited[e.Playlist] {
				return true
			}
		}
	}
	return false
}

// channelLabels — метки по очереди источников (спека этапа 8, раздел 5.4).
func channelLabels(c *Channel, o Override, in buildInput, l *Lineup, resolved map[string]labels.Labels) labels.Labels {
	var own labels.Source
	if o.Category != nil {
		own.Category = *o.Category
	}
	if o.Country != nil {
		own.Country = *o.Country
	}
	own.Languages = o.Languages
	var names, ids []string
	groups := map[string]int{}
	for _, s := range append(slices.Clone(c.Sources), c.Others...) {
		for _, e := range s.Entries {
			if id := orgID(e.TvgID); id != "" {
				ids = append(ids, id)
			}
			if e.Group != "" {
				groups[e.Group]++
			}
			if c.Name == "" && e.Name != "" {
				c.Name = e.Name
			}
			if c.Logo == "" && e.Logo != "" {
				c.Logo = e.Logo
			}
		}
	}
	epgNames := func(id string) []string {
		if in.epg.guide != nil {
			if ec, ok := in.epg.guide.Channel(id); ok {
				return ec.Names
			}
		}
		return nil
	}
	names = epgNames(c.EPGID)
	if len(names) == 0 && c.Name != "" {
		names = []string{c.Name}
	}
	sources := []labels.Source{own}
	if c.Zone == 0 {
		sources = append(sources, labels.FromOrg(in.base.Find(ids, names)))
	} else {
		// Региональная версия: сначала своя запись iptv-org (редко есть), потом метки канала без сдвига.
		sources = append(sources, labels.FromOrg(in.base.Find(nil, names)))
		baseID := strings.TrimSuffix(c.EPGID, "-pl"+strconv.Itoa(zoneOf(c.EPGID)))
		if bl, ok := resolved[baseID]; ok {
			sources = append(sources, labels.Source{Category: bl.Category, Country: bl.Country, Languages: bl.Languages})
		} else {
			sources = append(sources, labels.FromOrg(in.base.Find(nil, epgNames(baseID))))
		}
	}
	sources = append(sources, labels.FromID(c.EPGID, names), labels.FromGroups(groups))
	res := labels.Merge(sources...)
	// Ручная правка — окончательно, даже пустая: «Без категории», «Страна не указана», «Язык не указан».
	if o.Category != nil {
		res.Category = *o.Category
	}
	if o.Country != nil {
		res.Country = *o.Country
	}
	if o.Languages != nil {
		res.Languages = append([]string{}, o.Languages...)
	}
	return res
}

var qualityRank = map[string]int{"4K": 4, "FHD": 3, "HD": 2, "SD": 1}

// streamQuality — качество источника: по проверке, иначе лучшее по названиям записей.
func streamQuality(s *Stream) string {
	if s.Quality != "" {
		return s.Quality
	}
	best := ""
	for _, e := range s.Entries {
		if q := m3u.QualityOf(e.Name); qualityRank[q] > qualityRank[best] {
			best = q
		}
	}
	return best
}

func classRank(s *Stream) int {
	switch s.Grade {
	case GradeGreen:
		return 0
	case GradeYellow:
		return 1
	case GradeRed:
		return 3
	}
	return 2 // жив, полной проверки не было
}

// sortSources — порядок источников (спека этапа 8, раздел 5.9): закреплённый; без звука — после всех
// (отзыв заказчика 2026-09-30); оценка, доля хороших проверок за неделю, качество, время до данных.
func sortSources(c *Channel, share map[int64]float64) {
	ttfb := func(s *Stream) int {
		if s.TTFB <= 0 {
			return 1 << 30
		}
		return s.TTFB
	}
	slices.SortStableFunc(c.Sources, func(a, b *Stream) int {
		return cmp.Or(
			cmp.Compare(b2i(b.URL == c.Pinned && c.Pinned != ""), b2i(a.URL == c.Pinned && c.Pinned != "")),
			cmp.Compare(b2i(silent(a)), b2i(silent(b))),
			cmp.Compare(classRank(a), classRank(b)),
			cmp.Compare(share[b.ID], share[a.ID]),
			cmp.Compare(qualityRank[streamQuality(b)], qualityRank[streamQuality(a)]),
			cmp.Compare(ttfb(a), ttfb(b)),
			cmp.Compare(a.ID, b.ID),
		)
	})
	slices.SortStableFunc(c.Others, func(a, b *Stream) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortStableFunc(c.Rejected, func(a, b *Stream) int { return cmp.Compare(a.ID, b.ID) })
	c.Grade = ""
	if len(c.Sources) > 0 {
		c.Grade = c.Sources[0].Grade
		if c.Grade == "" || c.Grade == GradeAlive {
			c.Grade = GradeUnrated
		}
	}
}

// silent — проверка нашла, что звука нет.
func silent(s *Stream) bool { return s.Audio != nil && !*s.Audio }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// federalCandidates — версии федерального канала по порядку: под местный пояс, потом московская.
func federalCandidates(id string, local int, ix *epgIndex) []string {
	if local <= 0 {
		return []string{id}
	}
	n := strconv.Itoa(local)
	return []string{id + "-pl" + n, id + "+" + n, id}
}

// hiddenBy — почему канал скрыт (без учёта избранного — оно у каждого устройства своё).
func hiddenBy(c *Channel, o Override, in buildInput) string {
	switch {
	case o.Hidden:
		return HiddenChannel
	case c.Federal > 0:
		return "" // федеральный блок не слушается скрытия меток (спека, раздел 5.6)
	case slices.Contains(in.hidden.Categories, c.Labels.Category):
		return HiddenCategory
	case slices.Contains(in.hidden.Countries, c.Labels.Country):
		return HiddenCountry
	case anyHidden(c.Labels.Languages, in.hidden.Languages):
		return HiddenLanguage
	case in.hidden.OtherZones && c.Zone != 0 && c.Zone != in.localShift:
		return HiddenZone
	}
	return ""
}

// anyHidden — скрыт хотя бы один язык канала; без языков — скрыт «Язык не указан» (""). Решение
// заказчика этапа 11b (замечание № 6): «скрыл все языки, кроме русского» — двуязычные каналы тоже прячутся.
func anyHidden(langs, hidden []string) bool {
	if len(langs) == 0 {
		return slices.Contains(hidden, "")
	}
	return slices.ContainsFunc(langs, func(l string) bool { return slices.Contains(hidden, l) })
}

// compareChannels — порядок остальных каналов: категория, страна (сначала Россия), название; версия
// под местный пояс — перед московской.
func compareChannels(a, b *Channel, local int) int {
	cat := func(c *Channel) int { return slices.Index(labels.CategoryOrder, c.Labels.Category) }
	country := func(c *Channel) (int, string) {
		switch c.Labels.Country {
		case "RU":
			return 0, ""
		case "":
			return 2, ""
		}
		return 1, labels.CountryName(c.Labels.Country)
	}
	zone := func(c *Channel) int {
		switch c.Zone {
		case local:
			return 0
		case 0:
			return 1
		}
		return 2
	}
	ca, na := country(a)
	cb, nb := country(b)
	baseName := func(c *Channel) string {
		b, _ := m3u.SplitShift(m3u.Norm(c.Name))
		return b
	}
	return cmp.Or(cmp.Compare(cat(a), cat(b)), cmp.Compare(ca, cb), cmp.Compare(na, nb),
		cmp.Compare(baseName(a), baseName(b)), cmp.Compare(zone(a), zone(b)), cmp.Compare(a.Key, b.Key))
}

// WithFavorites — состав для устройства: сначала избранное (в его порядке), потом федеральные и
// остальные без повторов. all — со скрытыми.
func (l *Lineup) WithFavorites(favorites []string, all bool) []*Channel {
	out := []*Channel{}
	fav := map[string]bool{}
	for _, k := range favorites {
		if c := l.ByKey[k]; c != nil && c.Offered() && !fav[k] {
			fav[k] = true
			out = append(out, c)
		}
	}
	for _, c := range l.Order {
		if fav[c.Key] || (!all && c.Hidden != "") {
			continue
		}
		out = append(out, c)
	}
	return out
}
