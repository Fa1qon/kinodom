package iptv

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/httpx"
	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/xmltv"
	"kinodom/internal/player"
)

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
}

// Register — маршруты каналов (спека этапа 8, раздел 5.11). logo — отдача логотипа канала по адресу
// в интернете (кэш картинок приложения); nil — без логотипов.
func (m *Module) Register(r Router, logo func(w http.ResponseWriter, r *http.Request, src string)) {
	n := m.Name()
	r.Handle("GET /api/v1/channels", n, http.HandlerFunc(m.handleChannels))
	r.Handle("GET /api/v1/channels/{key}", n, http.HandlerFunc(m.handleChannel))
	r.Handle("GET /api/v1/channels/{key}/epg", n, http.HandlerFunc(m.handleEPG))
	r.Handle("GET /api/v1/channels/{key}/play", n, http.HandlerFunc(m.handlePlay))
	r.HandleHome("PUT /api/v1/channels/{key}", n, http.HandlerFunc(m.handleOverride))
	r.HandleHome("PUT /api/v1/iptv/favorites", n, http.HandlerFunc(m.handleFavorites))
	r.Handle("GET /api/v1/iptv/favorites", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys, err := m.Favorites(r.Context(), httpx.Device(r))
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "избранное не читается: "+err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	}))
	r.HandleHome("PUT /api/v1/iptv/favorites/{key}", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := m.AddFavorite(r.Context(), httpx.Device(r), r.PathValue("key")); err != nil {
			writeEditError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r.HandleHome("DELETE /api/v1/iptv/favorites/{key}", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := m.RemoveFavorite(r.Context(), httpx.Device(r), r.PathValue("key")); err != nil {
			writeEditError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r.Handle("GET /api/v1/iptv/playlists", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, m.Playlists())
	}))
	r.HandleHome("POST /api/v1/iptv/playlists", n, http.HandlerFunc(m.handleAddPlaylist))
	r.HandleHome("PUT /api/v1/iptv/playlists/{id}", n, http.HandlerFunc(m.handleUpdatePlaylist))
	r.HandleHome("DELETE /api/v1/iptv/playlists/{id}", n, http.HandlerFunc(m.handleDeletePlaylist))
	r.Handle("POST /api/v1/iptv/playlists/{id}/refresh", n, http.HandlerFunc(m.handleRefresh))
	r.Handle("GET /api/v1/iptv/unrecognized", n, http.HandlerFunc(m.handleUnrecognized))
	r.HandleHome("PUT /api/v1/iptv/names", n, http.HandlerFunc(m.handleNameRule))
	r.Handle("GET /api/v1/iptv/names", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]string{}
		if r.URL.Query().Get("hidden") == "1" {
			for _, name := range m.HiddenNames() {
				items = append(items, map[string]string{"name": name})
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	}))
	r.HandleHome("PUT /api/v1/iptv/streams/{id}", n, http.HandlerFunc(m.handleStreamRule))
	r.Handle("GET /api/v1/iptv/epg-channels", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": m.SearchEPG(r.URL.Query().Get("q"), 20)})
	}))
	r.Handle("POST /api/v1/iptv/probe", n, http.HandlerFunc(m.handleProbe))
	r.HandleHome("GET /api/v1/iptv/streams/{id}/watch", n, http.HandlerFunc(m.handleWatch)) // просмотр источника в пульте (план 14Д)
	r.HandleHome("GET /api/v1/iptv/relay", n, http.HandlerFunc(m.handleRelay))
	r.Handle("GET /m3u/channel/{file}", n, http.HandlerFunc(m.handleM3U))
	r.HandleHome("POST /api/v1/iptv/custom", n, http.HandlerFunc(m.handleCustom))
	r.Handle("GET /logo/{key}", n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := m.LogoURL(r.PathValue("key"))
		switch {
		case src == "":
			http.NotFound(w, r)
		case m.serveUploadedLogo(w, r, src): // свой канал с загруженным логотипом (план 14Д)
		case logo != nil:
			logo(w, r, src)
		default:
			http.NotFound(w, r)
		}
	}))
}

// ChannelView — канал для пульта и телевизора.
type ChannelView struct {
	Key           string           `json:"key"`          // ключ канала: страница, ★
	Version       string           `json:"version"`      // ключ версии: «Смотреть», .m3u8, программа, настройки
	VersionLabel  string           `json:"versionLabel"` // «МСК», «МСК+4», «МСК−1»
	VersionCount  int              `json:"versionCount"` // рабочих версий у канала: плеер приложения подписывает версию, если их несколько
	Name          string           `json:"name"`
	Logo          string           `json:"logo"`   // адрес логотипа на сервере; "" — нет
	Block         string           `json:"block"`  // favorite, federal, ""
	Number        int              `json:"number"` // номер кнопки федерального; 0 — нет
	Category      string           `json:"category"`
	CategoryName  string           `json:"categoryName"`
	Country       string           `json:"country"`
	CountryName   string           `json:"countryName"`
	Languages     []string         `json:"languages"`
	LanguageNames []string         `json:"languageNames"`
	Grade         string           `json:"grade"` // green, yellow, red, unrated
	Now           *xmltv.Programme `json:"now"`
	Next          *xmltv.Programme `json:"next"`
	Favorite      bool             `json:"favorite"`
	Hidden        string           `json:"hidden,omitempty"` // только с ?all=1
}

// Facet — категория, страна или язык с числом каналов.
type Facet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// VersionView — версия канала по времени на странице канала (спека 11b, 13.3).
type VersionView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// view — карточка версии c от имени её канала: ключ и имя — канала, «Смотреть» и программа — версии.
func (m *Module) view(l *Lineup, c *Channel, g *xmltv.Guide, at time.Time, fav bool) ChannelView {
	fk := l.FamilyOf[c.Key]
	if fk == "" {
		fk = familyOf(c.Key)
	}
	name := c.Name
	if f := l.Families[fk]; f != nil && f.Name != "" {
		name = f.Name
	}
	v := ChannelView{Key: fk, Version: c.Key, VersionLabel: VersionLabel(c.Zone), Name: name, Number: c.Federal, Category: c.Labels.Category,
		CategoryName: labels.CategoryName(c.Labels.Category), Country: c.Labels.Country,
		CountryName: labels.CountryName(c.Labels.Country), Languages: c.Labels.Languages, LanguageNames: []string{},
		Grade: c.Grade, Favorite: fav, Hidden: c.Hidden, VersionCount: 1}
	if f := l.Families[fk]; f != nil && len(f.Versions) > 1 {
		v.VersionCount = len(f.Versions)
	}
	if v.Languages == nil {
		v.Languages = []string{}
	}
	for _, l := range v.Languages {
		v.LanguageNames = append(v.LanguageNames, labels.LanguageName(l))
	}
	switch {
	case fav:
		v.Block = "favorite"
	case c.Federal > 0:
		v.Block = "federal"
	}
	if c.Logo != "" {
		v.Logo = "/logo/" + url.PathEscape(c.Key)
	}
	if g != nil {
		v.Now, v.Next = g.NowNext(c.EPGID, c.Shift, at)
	}
	return v
}

// handleChannels — состав для устройства: избранное, федеральные, остальные (спека этапа 8, 5.5).
func (m *Module) handleChannels(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	favs, err := m.Favorites(r.Context(), httpx.Device(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "избранное не читается: "+err.Error())
		return
	}
	isFav := map[string]bool{} // ★ — у канала; ключ версии (до 11b-Е) — тоже его канал
	for _, k := range favs {
		isFav[familyOf(k)] = true
	}
	l, g, at := m.Lineup(), m.Guide(), m.now()
	out := struct {
		Channels   []ChannelView `json:"channels"`
		Categories []Facet       `json:"categories"`
		Countries  []Facet       `json:"countries"`
		Languages  []Facet       `json:"languages"`
		UTCOffset  int           `json:"utcOffset"`
	}{Channels: []ChannelView{}, UTCOffset: m.utcOffset()}
	cats, countries, langs := map[string]int{}, map[string]int{}, map[string]int{}
	for _, c := range l.WithFavorites(favs, all) {
		v := m.view(l, c, g, at, isFav[l.FamilyOf[c.Key]])
		if v.Favorite {
			v.Hidden = "" // избранное сильнее скрытия
		}
		out.Channels = append(out.Channels, v)
		cats[v.Category]++
		countries[v.Country]++
		if len(v.Languages) == 0 {
			langs[""]++
		}
		for _, x := range v.Languages {
			langs[x]++
		}
	}
	for _, id := range labels.CategoryOrder {
		if n := cats[id]; n > 0 {
			out.Categories = append(out.Categories, Facet{id, labels.CategoryName(id), n})
		}
	}
	if !all {
		// Скрытое в настройках в фильтрах не показывается, даже если канал с ним виден (избранное,
		// федеральный блок) — замечание № 6 этапа 11b. ?all=1 — экран скрытия: там нужно всё.
		h := m.hiddenNow()
		for _, id := range h.Countries {
			delete(countries, id)
		}
		for _, id := range h.Languages {
			delete(langs, id)
		}
	}
	out.Countries = facets(countries, labels.CountryName)
	out.Languages = facets(langs, labels.LanguageName)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// facets — по убыванию числа каналов, при равенстве — по названию; «не указана» — последней.
func facets(n map[string]int, name func(string) string) []Facet {
	out := []Facet{}
	for id, c := range n {
		out = append(out, Facet{id, name(id), c})
	}
	slices.SortFunc(out, func(a, b Facet) int {
		return cmp.Or(cmp.Compare(b2i(a.ID == ""), b2i(b.ID == "")), cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// SourceView — источник в карточке канала.
type SourceView struct {
	ID        int64      `json:"id"`
	URL       string     `json:"url"`
	Name      string     `json:"name"` // название записи в плейлисте
	Playlists []string   `json:"playlists"`
	Kind      string     `json:"kind"`
	Quality   string     `json:"quality"`
	State     string     `json:"state"`
	Grade     string     `json:"grade"`
	CheckedAt *time.Time `json:"checkedAt"`
	TTFBMs    int        `json:"ttfbMs"`
	Ratio     float64    `json:"ratio"`
	Mbps      float64    `json:"mbps"`
	Error     string     `json:"error"`
	Week      Week       `json:"week"`
	Pinned    bool       `json:"pinned"`
	Offered   bool       `json:"offered"`
	Hidden    bool       `json:"hidden"` // скрыт у канала вручную
	Audio     *bool      `json:"audio"`  // есть ли звук; null — не знаем
}

// OverrideView — правки канала; null — не правили.
type OverrideView struct {
	Hidden    bool     `json:"hidden"`
	Category  *string  `json:"category"`
	Country   *string  `json:"country"`
	Languages []string `json:"languages"`
}

// handleChannel — страница канала: ключ канала — версия по умолчанию, ?version= — выбранная версия того же
// канала (ключ канала совпадает с ключом московской версии — Review Focus 5 плана 11b-Е).
func (m *Module) handleChannel(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	l := m.Lineup()
	c := l.Resolve(key)
	if v := r.URL.Query().Get("version"); v != "" {
		if c = l.ByKey[v]; c != nil && familyOf(v) != familyOf(key) {
			c = nil
		}
	}
	if c == nil {
		httpx.WriteError(w, http.StatusNotFound, ErrNoChannel.Error())
		return
	}
	fk := l.FamilyOf[c.Key]
	favs, err := m.Favorites(r.Context(), httpx.Device(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "избранное не читается: "+err.Error())
		return
	}
	g, at := m.Guide(), m.now()
	fav := slices.ContainsFunc(favs, func(k string) bool { return familyOf(k) == fk })
	out := struct {
		ChannelView
		Versions  []VersionView     `json:"versions"` // рабочие версии канала по сдвигу
		Sources   []SourceView      `json:"sources"`
		Override  OverrideView      `json:"override"`
		Programme []xmltv.Programme `json:"programme"`
		UTCOffset int               `json:"utcOffset"`
	}{ChannelView: m.view(l, c, g, at, fav), Versions: []VersionView{}, Sources: []SourceView{}, Programme: []xmltv.Programme{}, UTCOffset: m.utcOffset()}
	if f := l.Families[fk]; f != nil {
		for _, v := range f.Versions {
			out.Versions = append(out.Versions, VersionView{Key: v.Key, Label: VersionLabel(v.Zone)})
		}
	}
	all := slices.Concat(c.Sources, c.Others, c.Rejected)
	ids := make([]int64, len(all))
	for i, s := range all {
		ids[i] = s.ID
	}
	weeks, err := m.Weeks(r.Context(), ids)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "проверки не читаются: "+err.Error())
		return
	}
	m.mu.Lock()
	names := map[int64]string{}
	for id, pl := range m.pool.playlists {
		names[id] = pl.Name
	}
	for i, s := range all {
		sv := SourceView{ID: s.ID, URL: s.URL, Playlists: []string{}, Kind: s.Kind, Quality: streamQuality(s), State: s.State,
			Grade: s.Grade, TTFBMs: s.TTFB, Ratio: s.Ratio, Mbps: s.Mbps, Error: s.Error, Week: weeks[s.ID],
			Pinned: c.Pinned != "" && s.URL == c.Pinned, Offered: i < len(c.Sources), Audio: s.Audio}
		if s.FullAt.After(s.LightAt) {
			sv.CheckedAt = timePtr(s.FullAt)
		} else {
			sv.CheckedAt = timePtr(s.LightAt)
		}
		for _, e := range s.Entries {
			if sv.Name == "" {
				sv.Name = e.Name
			}
			if n := names[e.Playlist]; n != "" && !slices.Contains(sv.Playlists, n) {
				sv.Playlists = append(sv.Playlists, n)
			}
		}
		out.Sources = append(out.Sources, sv)
	}
	o := m.pool.overrides[c.Key]                 // источники — у версии
	lo := labelOverride(m.pool.overrides, c.Key) // метки — у канала (раньше — у версии)
	hidden := m.pool.overrides[fk].Hidden        // «Скрыть канал» — у канала
	m.mu.Unlock()
	for i := range out.Sources { // скрытый может быть и предлагаемым — запасным, когда других нет
		out.Sources[i].Hidden = slices.Contains(o.HiddenURLs, out.Sources[i].URL)
	}
	out.Override = OverrideView{Hidden: hidden, Category: lo.Category, Country: lo.Country, Languages: lo.Languages}
	if g != nil {
		loc := m.Location()
		day := at.In(loc)
		from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		if ps := g.Programmes(c.EPGID, c.Shift, from, from.AddDate(0, 0, 1)); ps != nil {
			out.Programme = ps
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleEPG — программа на день: ?date=2026-09-29 (день по часовому поясу каналов); без даты — сегодня.
func (m *Module) handleEPG(w http.ResponseWriter, r *http.Request) {
	c := m.channelOrVersion(r.PathValue("key"))
	if c == nil {
		httpx.WriteError(w, http.StatusNotFound, ErrNoChannel.Error())
		return
	}
	loc := m.Location()
	day := m.now().In(loc)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	if d := r.URL.Query().Get("date"); d != "" {
		t, err := time.ParseInLocation(time.DateOnly, d, loc)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "дата — в виде 2026-09-29")
			return
		}
		from = t
	}
	items := []xmltv.Programme{}
	if g := m.Guide(); g != nil {
		if ps := g.Programmes(c.EPGID, c.Shift, from, from.AddDate(0, 0, 1)); ps != nil {
			items = ps
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "utcOffset": m.utcOffset()})
}

// utcOffset — пояс каналов в часах от UTC: пульт показывает время программы по нему, а не по часам
// устройства (отзыв заказчика 2026-09-30: на ПК московское время, в настройках UTC+7).
func (m *Module) utcOffset() int {
	_, off := m.now().In(m.Location()).Zone()
	return off / 3600
}

// maxPlay — сколько источников отдавать плееру (спека этапа 8, раздел 5.9).
const maxPlay = 5

// PlayItem — воспроизводимое в общем виде основной спеки (раздел 13), с заголовками и качеством.
type PlayItem struct {
	URL     string      `json:"url"`
	Headers m3u.Headers `json:"headers"`
	Title   string      `json:"title"`
	Kind    string      `json:"kind"`
	Quality string      `json:"quality"`
}

func (m *Module) playItems(c *Channel) []PlayItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []PlayItem{}
	for _, s := range c.Sources {
		if len(out) == maxPlay {
			break
		}
		it := PlayItem{URL: s.URL, Title: c.Name, Kind: s.Kind, Quality: streamQuality(s)}
		for _, e := range s.Entries {
			if e.Headers != (m3u.Headers{}) {
				it.Headers = e.Headers
				break
			}
		}
		if it.Kind == "" {
			it.Kind = "live"
		}
		out = append(out, it)
	}
	return out
}

// channelOrVersion — версия по ключу версии («Смотреть», .m3u8, программа — по ней); ключ канала, который сам
// не версия, — версия по умолчанию.
func (m *Module) channelOrVersion(key string) *Channel {
	l := m.Lineup()
	if c := l.ByKey[key]; c != nil {
		return c
	}
	return l.Resolve(key)
}

// handlePlay — «Смотреть» (спека этапа 8, раздел 5.9): источники по порядку, .m3u8 для VLC и ссылка
// kinodom:// для этого ПК.
func (m *Module) handlePlay(w http.ResponseWriter, r *http.Request) {
	c := m.channelOrVersion(r.PathValue("key"))
	if c == nil {
		httpx.WriteError(w, http.StatusNotFound, ErrNoChannel.Error())
		return
	}
	items := m.playItems(c)
	if len(items) == 0 {
		httpx.WriteError(w, http.StatusConflict, "у канала сейчас нет работающих источников")
		return
	}
	path := "/m3u/channel/" + url.PathEscape(c.Key) + ".m3u8"
	out := struct {
		Items     []PlayItem `json:"items"`
		Title     string     `json:"title"`
		M3UURL    string     `json:"m3uUrl"`
		LaunchURL *string    `json:"launchUrl"`
	}{Items: items, Title: c.Name, M3UURL: "http://" + r.Host + path}
	if httpx.FromThisPC(r) {
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			l := player.LaunchURL("http://127.0.0.1:"+port+path, c.Name)
			out.LaunchURL = &l
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleM3U — /m3u/channel/{ключ}.m3u8: источники канала для VLC.
func (m *Module) handleM3U(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutSuffix(r.PathValue("file"), ".m3u8")
	c := m.channelOrVersion(key)
	if !ok || c == nil {
		httpx.WriteError(w, http.StatusNotFound, ErrNoChannel.Error())
		return
	}
	items := m.playItems(c)
	if len(items) == 0 {
		httpx.WriteError(w, http.StatusConflict, "у канала сейчас нет работающих источников")
		return
	}
	list := make([]player.M3UItem, len(items))
	for i, it := range items {
		list[i] = player.M3UItem{Title: it.Title, URL: it.URL, UserAgent: it.Headers.UserAgent, Referrer: it.Headers.Referrer}
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", player.M3UDisposition(c.Name))
	w.Write(player.M3UList(list))
}

// optional — поле правки: нет в запросе (Set = false), null или значение.
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// handleOverride — правки канала из карточки: скрыть, категория, страна, языки, основной (закреплённый)
// источник, скрыть и вернуть источник.
func (m *Module) handleOverride(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req struct {
		Hidden       *bool              `json:"hidden"`
		Category     optional[string]   `json:"category"`
		Country      optional[string]   `json:"country"`
		Languages    optional[[]string] `json:"languages"`
		PinnedSource optional[int64]    `json:"pinnedSource"`
		HideSource   *int64             `json:"hideSource"`
		ShowSource   *int64             `json:"showSource"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	// «Скрыть канал» и метки — у канала, источники — у версии (спека 11b, 13.1). У московской версии ключ
	// тот же, что у канала, — одна правка.
	fk := familyOf(key)
	o := m.Override(fk)
	// Метки правились раньше у версии (до 11b-Е): при первой правке меток на канале они переезжают на канал —
	// иначе «Как было» не снимало бы их, а правка одного поля теряла бы остальные (финальное ревью 11b-Е).
	labelsChanged := req.Category.Set || req.Country.Set || req.Languages.Set
	migrated := false
	if labelsChanged && fk != key && o.Category == nil && o.Country == nil && o.Languages == nil {
		if v := m.Override(key); v.Category != nil || v.Country != nil || v.Languages != nil {
			o.Category, o.Country, o.Languages = v.Category, v.Country, v.Languages
			migrated = true
		}
	}
	if req.Hidden != nil {
		o.Hidden = *req.Hidden
	}
	if req.Category.Set {
		if v := req.Category.Value; v != nil {
			if err := CheckCategory(*v); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		o.Category = req.Category.Value
	}
	if req.Country.Set {
		if v := req.Country.Value; v != nil {
			if err := CheckCountry(*v); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		o.Country = req.Country.Value
	}
	if req.Languages.Set {
		o.Languages = nil
		if v := req.Languages.Value; v != nil {
			o.Languages = []string{}
			for _, l := range *v {
				if err := CheckLanguage(l); err != nil || l == "" {
					httpx.WriteError(w, http.StatusBadRequest, "язык — код из трёх строчных латинских букв, например rus")
					return
				}
				o.Languages = append(o.Languages, l)
			}
		}
	}
	family := o
	familyChanged := req.Hidden != nil || labelsChanged
	if fk != key {
		o = m.Override(key)
		if migrated {
			o.Category, o.Country, o.Languages = nil, nil, nil
		}
	}
	if req.PinnedSource.Set {
		o.PinnedURL = ""
		if v := req.PinnedSource.Value; v != nil {
			if o.PinnedURL = m.StreamURL(*v); o.PinnedURL == "" {
				httpx.WriteError(w, http.StatusNotFound, ErrNoStream.Error())
				return
			}
		}
	}
	for _, id := range []*int64{req.HideSource, req.ShowSource} {
		if id != nil && m.StreamURL(*id) == "" {
			httpx.WriteError(w, http.StatusNotFound, ErrNoStream.Error())
			return
		}
	}
	if req.HideSource != nil {
		u := m.StreamURL(*req.HideSource)
		if !slices.Contains(o.HiddenURLs, u) {
			o.HiddenURLs = append(slices.Clone(o.HiddenURLs), u)
		}
		if o.PinnedURL == u {
			o.PinnedURL = "" // скрытый не может быть основным
		}
	}
	if req.ShowSource != nil {
		u := m.StreamURL(*req.ShowSource)
		o.HiddenURLs = slices.DeleteFunc(slices.Clone(o.HiddenURLs), func(x string) bool { return x == u })
	}
	versionChanged := req.PinnedSource.Set || req.HideSource != nil || req.ShowSource != nil || migrated
	if fk != key && familyChanged {
		if err := m.SetOverride(r.Context(), fk, family); err != nil {
			writeEditError(w, err)
			return
		}
	}
	if fk == key || versionChanged {
		if err := m.SetOverride(r.Context(), key, o); err != nil {
			writeEditError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCustom — «Новый канал» (план 14Д): {name, logo, logoData, category, country, group} → {key}.
func (m *Module) handleCustom(w http.ResponseWriter, r *http.Request) {
	var in CustomInput
	// Логотип до 1 МБ в base64 — до 1,4 МБ: предел запроса больше обычного.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "не удалось разобрать запрос: "+err.Error())
		return
	}
	key, err := m.CreateCustom(r.Context(), in)
	if err != nil {
		writeEditError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"key": key})
}

func writeEditError(w http.ResponseWriter, err error) {
	var fe *FieldError
	switch {
	case errors.As(err, &fe):
		httpx.WriteError(w, http.StatusBadRequest, fe.Error())
	case errors.Is(err, ErrNoChannel), errors.Is(err, ErrNoStream), errors.Is(err, errNoPlaylist):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrBadPlaylist):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}

func (m *Module) handleFavorites(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []string `json:"keys"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	if err := m.SetFavorites(r.Context(), httpx.Device(r), req.Keys); err != nil {
		writeEditError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxPlaylistBody — плейлист файлом приходит в JSON в base64 (изменяющие запросы — только JSON).
const maxPlaylistBody = maxPlaylist*4/3 + 1<<20

// readBig — ReadJSON с большим пределом тела.
func readBig(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlaylistBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "файл больше 32 МБ")
			return false
		}
		httpx.WriteError(w, http.StatusBadRequest, "не удалось разобрать запрос: "+err.Error())
		return false
	}
	return true
}

func decodeData(s *string) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(*s)
	if err != nil {
		return nil, &FieldError{"файл плейлиста не читается (нужен base64)"}
	}
	return b, nil
}

func (m *Module) handleAddPlaylist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		URL     string  `json:"url"`
		Data    *string `json:"data"`
		Limited bool    `json:"limited"`
	}
	if !readBig(w, r, &req) {
		return
	}
	data, err := decodeData(req.Data)
	if err != nil {
		writeEditError(w, err)
		return
	}
	// Скачивание ссылки — до минуты: не зависит от того, что браузер закрыли.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	res, err := m.AddPlaylist(ctx, PlaylistInput{Name: req.Name, URL: req.URL, Data: data, Limited: req.Limited})
	if err != nil {
		if errors.Is(err, ErrBadPlaylist) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "Плейлист не добавлен: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

func playlistID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер плейлиста")
		return 0, false
	}
	return id, true
}

func (m *Module) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Limited *bool   `json:"limited"`
		Data    *string `json:"data"`
	}
	if !readBig(w, r, &req) {
		return
	}
	data, err := decodeData(req.Data)
	if err != nil {
		writeEditError(w, err)
		return
	}
	res, err := m.UpdatePlaylist(r.Context(), id, PlaylistPatch{Name: req.Name, Limited: req.Limited, Data: data})
	if err != nil {
		writeEditError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistID(w, r)
	if !ok {
		return
	}
	if err := m.DeletePlaylist(r.Context(), id); err != nil {
		writeEditError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRefresh — «Обновить» плейлист по ссылке: в фоне, ответ сразу.
func (m *Module) handleRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := playlistID(w, r)
	if !ok {
		return
	}
	m.mu.Lock()
	pl := m.pool.playlists[id]
	m.mu.Unlock()
	switch {
	case pl == nil:
		httpx.WriteError(w, http.StatusNotFound, errNoPlaylist.Error())
		return
	case pl.URL == "":
		httpx.WriteError(w, http.StatusConflict, "плейлист загружен файлом — загрузите файл заново")
		return
	}
	m.background(func(ctx context.Context) {
		if err := m.RefreshPlaylist(ctx, id); err != nil {
			m.log.Warn("iptv: плейлист не обновился", "playlist", id, "err", err)
		}
	})
	httpx.WriteJSON(w, http.StatusAccepted, struct{}{})
}

// UnrecognizedView — строка «Не распознано».
type UnrecognizedView struct {
	Name      string   `json:"name"`
	Sample    string   `json:"sample"`
	Streams   int      `json:"streams"`
	Alive     int      `json:"alive"`
	Playlists []string `json:"playlists"`
	// Watch — поток для «Смотреть» (план 14Д): живой, если есть, иначе первый; WatchURL и WatchKind — его
	// адрес и вид (пульт выбирает плеер).
	Watch     int64  `json:"watch"`
	WatchURL  string `json:"watchUrl"`
	WatchKind string `json:"watchKind"`
}

// unrecognizedPage — строк на странице «Не распознано».
const unrecognizedPage = 50

func (m *Module) handleUnrecognized(w http.ResponseWriter, r *http.Request) {
	q := m3u.Norm(r.URL.Query().Get("q"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	l := m.Lineup()
	var matched []Group
	for _, g := range l.Unrecognized {
		if q == "" || strings.Contains(g.Name, q) {
			matched = append(matched, g)
		}
	}
	m.mu.Lock()
	names := map[int64]string{}
	for id, pl := range m.pool.playlists {
		names[id] = pl.Name
	}
	streams := m.pool.streams
	watch := func(g Group) (int64, string, string) {
		var first *Stream
		for _, id := range g.Streams {
			s := streams[id]
			if s == nil {
				continue
			}
			if s.State == StateAlive {
				return s.ID, s.URL, s.Kind
			}
			if first == nil {
				first = s
			}
		}
		if first == nil {
			return 0, "", ""
		}
		return first.ID, first.URL, first.Kind
	}
	m.mu.Unlock()
	out := struct {
		Items []UnrecognizedView `json:"items"`
		Total int                `json:"total"`
		Pages int                `json:"pages"`
	}{Items: []UnrecognizedView{}, Total: len(matched), Pages: (len(matched) + unrecognizedPage - 1) / unrecognizedPage}
	start := (page - 1) * unrecognizedPage
	for i := start; i < len(matched) && i < start+unrecognizedPage; i++ {
		g := matched[i]
		v := UnrecognizedView{Name: g.Name, Sample: g.Sample, Streams: len(g.Streams), Alive: g.Alive, Playlists: []string{}}
		m.mu.Lock()
		v.Watch, v.WatchURL, v.WatchKind = watch(g)
		m.mu.Unlock()
		for _, p := range g.Playlists {
			if n := names[p]; n != "" {
				v.Playlists = append(v.Playlists, n)
			}
		}
		out.Items = append(out.Items, v)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleNameRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		Channel *string `json:"channel"`
		Hidden  bool    `json:"hidden"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	ch := ""
	if req.Channel != nil {
		ch = *req.Channel
	}
	if err := m.SetNameRule(r.Context(), req.Name, ch, req.Hidden); err != nil {
		writeEditError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleStreamRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер источника")
		return
	}
	var req struct {
		Channel *string `json:"channel"`
		Hidden  bool    `json:"hidden"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	ch := ""
	if req.Channel != nil {
		ch = *req.Channel
	}
	if err := m.SetStreamRule(r.Context(), id, ch, req.Hidden); err != nil {
		writeEditError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProbe — «Проверить»: канал или плейлист, в фоне.
func (m *Module) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel  string `json:"channel"`
		Playlist int64  `json:"playlist"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	ok := false
	switch {
	case req.Channel != "":
		ok = m.ProbeChannel(req.Channel)
	case req.Playlist != 0:
		ok = m.ProbePlaylist(req.Playlist)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "нужен канал или плейлист")
		return
	}
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "такого канала или плейлиста нет")
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, struct{}{})
}
