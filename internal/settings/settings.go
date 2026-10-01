// Package settings — настройки, которые меняются в пульте (спека этапа 7, раздел 5.1): чтение из
// базы, проверка изменений, запись и применение к работающим модулям без перезапуска.
package settings

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/store"
)

// Ключи в таблице settings. Имена — прежние: базы этапов 2–6 читаются без миграции.
const (
	KeyRutrackerLogin    = "rutracker.login"
	KeyRutrackerPassword = "rutracker.password"
	// Адреса трекеров вводит пользователь (спека этапа 11a, раздел 6); служебные "" — по правилу.
	KeyRutrackerAddress = "rutracker.address"
	KeyRutrackerAPI     = "rutracker.apiAddress"
	KeyRutrackerFeed    = "rutracker.feedAddress"
	KeyRutorAddress     = "rutor.address"
	KeyRutorDownload    = "rutor.downloadAddress"
	KeySetupDone        = "setup.done" // мастер начальных настроек пройден
	KeyProxy            = "proxy.trackers"
	KeyKinopoisk        = "kinopoisk.key"
	KeySearchAddress    = "search.address" // источник поиска Jacred / Jackett (спека 11b, раздел 8)
	KeySearchKey        = "search.key"
	KeyDownloadsDir     = "downloads.dir"
	KeyKeepDays         = "torrents.keepDays"
	KeyKeepBehind       = "torrents.keepBehind"
	KeyMinFreeGB        = "torrents.minFreeGB"
	KeyUploadLimit      = "torrents.uploadLimitMBps"
	KeyPlayer           = "player"
	KeySections         = "catalog.categories"
	KeyPreferredFormat  = "catalog.preferredFormat"
	// Каналы (спека этапа 8, раздел 5.6); списки — JSON.
	KeyEPGURL           = "iptv.epgUrl"
	KeyHiddenCategories = "iptv.hiddenCategories"
	KeyHiddenCountries  = "iptv.hiddenCountries"
	KeyHiddenLanguages  = "iptv.hiddenLanguages"
	KeyHideOtherZones   = "iptv.hideOtherZones"
	KeyUTCOffset        = "iptv.utcOffset"
)

// Значения по умолчанию (основная спека, раздел 15).
const (
	DefaultKeepDays   = 14
	DefaultKeepBehind = 1
	DefaultMinFreeGB  = 20
	DefaultUploadMBps = 2
	DefaultPlayer     = "auto"
	DefaultUTCOffset  = 7 // часовой пояс каналов: UTC+7, как у заказчика
)

// Players — плееры на этом ПК: auto — VLC, если нет — MPC-HC.
var Players = []string{"auto", "vlc", "mpc-hc"}

// Formats — форматы, которые можно поставить в приоритет; "" — нет (спека этапа 7, раздел 10.3).
var Formats = []string{"", "MKV", "MP4", "AVI"}

// Values — действующие настройки.
type Values struct {
	RutrackerLogin    string
	RutrackerPassword string
	RutrackerAddress  string // «схема://хост»; "" — Rutracker выключен
	RutrackerAPI      string // "" — по правилу из адреса сайта
	RutrackerFeed     string // "" — по правилу из адреса сайта
	RutorAddress      string // «схема://хост»; "" — Rutor выключен
	RutorDownload     string // адрес .torrent; "" — по правилу из адреса сайта
	SetupDone         bool
	Proxy             string // адрес целиком: http:// или socks5://, с логином и паролем; "" — нет
	KinopoiskKey      string
	SearchAddress     string // источник поиска: «схема://хост»; "" — выключен
	SearchKey         string
	DownloadsDir      string
	KeepDays          int
	KeepBehind        int // серий позади просмотренной оставлять, когда места не хватает
	MinFreeGB         int
	UploadMBps        *float64 // nil — без ограничения; 0 — не раздавать
	Player            string
	Sections          string // «rutracker:2110,rutracker:46+,rutor:12» (формат — пакет catalog)
	PreferredFormat   string // формат в приоритете: "", MKV, MP4, AVI
	EPGURL            string // телепрограмма; "" — по умолчанию
	HiddenCategories  []string
	HiddenCountries   []string
	HiddenLanguages   []string
	HideOtherZones    bool
	UTCOffset         int // часовой пояс каналов, часы от UTC
}

// Defaults — то, чего пакет не знает сам: папка загрузок по умолчанию и разделы каталога по умолчанию.
type Defaults struct {
	DownloadsDir string
	Sections     string
}

// Load читает настройки из базы; overrides — поверх базы и не сохраняются (тесты, kinodom catalog:
// логин, пароль и ключ из переменных окружения). Нечитаемое число — значение по умолчанию: служба
// должна стартовать при любой базе.
func Load(ctx context.Context, db *store.DB, def Defaults, overrides map[string]string) (Values, error) {
	get := func(key string) (string, bool, error) {
		if v, ok := overrides[key]; ok && v != "" {
			return v, true, nil
		}
		return db.Setting(ctx, key)
	}
	str := func(key, fallback string) (string, error) {
		v, ok, err := get(key)
		if err != nil || !ok || v == "" {
			return fallback, err
		}
		return v, nil
	}
	num := func(key string, fallback, least int) (int, error) {
		s, err := str(key, "")
		if err != nil || s == "" {
			return fallback, err
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < least {
			return fallback, nil
		}
		return n, nil
	}
	var v Values
	var errs []error
	collect := func(err error) { errs = append(errs, err) }
	var err error
	v.RutrackerLogin, err = str(KeyRutrackerLogin, "")
	collect(err)
	v.RutrackerPassword, err = str(KeyRutrackerPassword, "")
	collect(err)
	for key, dst := range map[string]*string{KeyRutrackerAddress: &v.RutrackerAddress, KeyRutrackerAPI: &v.RutrackerAPI,
		KeyRutrackerFeed: &v.RutrackerFeed, KeyRutorAddress: &v.RutorAddress, KeyRutorDownload: &v.RutorDownload} {
		*dst, err = str(key, "")
		collect(err)
	}
	done, err := str(KeySetupDone, "false")
	collect(err)
	v.SetupDone = done == "true"
	v.Proxy, err = str(KeyProxy, "")
	collect(err)
	v.KinopoiskKey, err = str(KeyKinopoisk, "")
	collect(err)
	v.SearchAddress, err = str(KeySearchAddress, "")
	collect(err)
	v.SearchKey, err = str(KeySearchKey, "")
	collect(err)
	v.DownloadsDir, err = str(KeyDownloadsDir, def.DownloadsDir)
	collect(err)
	v.KeepDays, err = num(KeyKeepDays, DefaultKeepDays, 1)
	collect(err)
	v.KeepBehind, err = num(KeyKeepBehind, DefaultKeepBehind, 0)
	collect(err)
	v.MinFreeGB, err = num(KeyMinFreeGB, DefaultMinFreeGB, 0)
	collect(err)
	v.Player, err = str(KeyPlayer, DefaultPlayer)
	collect(err)
	if !slices.Contains(Players, v.Player) {
		v.Player = DefaultPlayer
	}
	v.Sections, err = str(KeySections, def.Sections)
	collect(err)
	v.PreferredFormat, err = str(KeyPreferredFormat, "")
	collect(err)
	if !slices.Contains(Formats, v.PreferredFormat) {
		v.PreferredFormat = ""
	}
	v.EPGURL, err = str(KeyEPGURL, "")
	collect(err)
	list := func(key string, fallback []string) ([]string, error) {
		s, ok, err := get(key)
		var out []string
		if err != nil || !ok || json.Unmarshal([]byte(s), &out) != nil || out == nil {
			return fallback, err
		}
		return out, nil
	}
	v.HiddenCategories, err = list(KeyHiddenCategories, []string{"adult"})
	collect(err)
	v.HiddenCountries, err = list(KeyHiddenCountries, []string{})
	collect(err)
	v.HiddenLanguages, err = list(KeyHiddenLanguages, []string{})
	collect(err)
	zones, err := str(KeyHideOtherZones, "true")
	collect(err)
	v.HideOtherZones = zones != "false"
	v.UTCOffset, err = num(KeyUTCOffset, DefaultUTCOffset, -12)
	collect(err)
	if v.UTCOffset > 14 {
		v.UTCOffset = DefaultUTCOffset
	}
	// Лимит отдачи: строки нет — 2 МБ/с; пустая строка — без ограничения (так её пишет пульт).
	up, ok, err := get(KeyUploadLimit)
	collect(err)
	switch f, perr := strconv.ParseFloat(up, 64); {
	case !ok:
		v.UploadMBps = ptr(float64(DefaultUploadMBps))
	case up == "":
		v.UploadMBps = nil
	case perr != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0):
		v.UploadMBps = ptr(float64(DefaultUploadMBps))
	default:
		v.UploadMBps = &f
	}
	return v, errors.Join(errs...)
}

func ptr[T any](v T) *T { return &v }

func jsonList(l []string) string {
	if l == nil {
		l = []string{}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// entries — настройки в виде строк базы.
func (v Values) entries() map[string]string {
	up := ""
	if v.UploadMBps != nil {
		up = strconv.FormatFloat(*v.UploadMBps, 'f', -1, 64)
	}
	return map[string]string{
		KeyRutrackerLogin:    v.RutrackerLogin,
		KeyRutrackerPassword: v.RutrackerPassword,
		KeyRutrackerAddress:  v.RutrackerAddress,
		KeyRutrackerAPI:      v.RutrackerAPI,
		KeyRutrackerFeed:     v.RutrackerFeed,
		KeyRutorAddress:      v.RutorAddress,
		KeyRutorDownload:     v.RutorDownload,
		KeySetupDone:         strconv.FormatBool(v.SetupDone),
		KeyProxy:             v.Proxy,
		KeyKinopoisk:         v.KinopoiskKey,
		KeySearchAddress:     v.SearchAddress,
		KeySearchKey:         v.SearchKey,
		KeyDownloadsDir:      v.DownloadsDir,
		KeyKeepDays:          strconv.Itoa(v.KeepDays),
		KeyKeepBehind:        strconv.Itoa(v.KeepBehind),
		KeyMinFreeGB:         strconv.Itoa(v.MinFreeGB),
		KeyUploadLimit:       up,
		KeyPlayer:            v.Player,
		KeySections:          v.Sections,
		KeyPreferredFormat:   v.PreferredFormat,
		KeyEPGURL:            v.EPGURL,
		KeyHiddenCategories:  jsonList(v.HiddenCategories),
		KeyHiddenCountries:   jsonList(v.HiddenCountries),
		KeyHiddenLanguages:   jsonList(v.HiddenLanguages),
		KeyHideOtherZones:    strconv.FormatBool(v.HideOtherZones),
		KeyUTCOffset:         strconv.Itoa(v.UTCOffset),
	}
}

// save записывает в базу только то, что изменилось: значения из overrides (тесты) не попадают в
// базу, пока их не поменяли в пульте.
func save(ctx context.Context, db *store.DB, old, new Values) error {
	was, now := old.entries(), new.entries()
	keys := make([]string, 0, len(now))
	for k := range now {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if was[k] == now[k] {
			continue
		}
		if err := db.SetSetting(ctx, k, now[k]); err != nil {
			return fmt.Errorf("настройки не сохранились: %w", err)
		}
	}
	return nil
}

// FieldError — неверное значение поля: текст для человека называет поле.
type FieldError struct {
	Field string // «Прокси», «Хранить, дней»
	Text  string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Text }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Text: fmt.Sprintf(format, args...)}
}

// With — настройки после изменений из пульта. Проверяет всё, что проверяется без модулей;
// первая же ошибка — отказ, прежние значения не меняются.
func (v Values) With(p Patch) (Values, error) {
	n := v
	if r := p.Rutracker; r != nil {
		if r.Login != nil {
			n.RutrackerLogin = strings.TrimSpace(*r.Login)
		}
		if r.Password != nil {
			n.RutrackerPassword = *r.Password
		}
		for _, f := range []struct {
			in    *string
			dst   *string
			field string
		}{{r.Address, &n.RutrackerAddress, "Адрес Rutracker"}, {r.APIAddress, &n.RutrackerAPI, "Адрес API Rutracker"},
			{r.FeedAddress, &n.RutrackerFeed, "Адрес ленты Rutracker"}} {
			if err := setAddress(f.in, f.dst, f.field); err != nil {
				return v, err
			}
		}
	}
	if r := p.Rutor; r != nil {
		if err := setAddress(r.Address, &n.RutorAddress, "Адрес Rutor"); err != nil {
			return v, err
		}
		if err := setAddress(r.DownloadAddress, &n.RutorDownload, "Адрес .torrent Rutor"); err != nil {
			return v, err
		}
	}
	if st := p.Setup; st != nil && st.Done != nil {
		n.SetupDone = *st.Done
	}
	if pp := p.Proxy; pp != nil {
		proxy, err := composeProxy(v.Proxy, *pp)
		if err != nil {
			return v, err
		}
		n.Proxy = proxy
	}
	if k := p.Kinopoisk; k != nil && k.Key != nil {
		n.KinopoiskKey = strings.TrimSpace(*k.Key)
	}
	if sp := p.Search; sp != nil {
		if sp.Address != nil {
			a, err := searchAddress(*sp.Address)
			if err != nil {
				return v, fieldErr("Адрес источника поиска", "%v", err)
			}
			n.SearchAddress = a
		}
		if sp.Key != nil {
			n.SearchKey = strings.TrimSpace(*sp.Key)
		}
	}
	if s := p.Storage; s != nil {
		if s.DownloadsDir != nil {
			dir := strings.TrimSpace(*s.DownloadsDir)
			if dir == "" || !filepath.IsAbs(dir) {
				return v, fieldErr("Папка загрузок", `нужен полный путь, например D:\Kinodom`)
			}
			n.DownloadsDir = filepath.Clean(dir)
		}
		if s.KeepDays != nil {
			if *s.KeepDays < 1 {
				return v, fieldErr("Хранить, дней", "нужно целое число от 1")
			}
			n.KeepDays = *s.KeepDays
		}
		if s.KeepBehind != nil {
			if *s.KeepBehind < 0 {
				return v, fieldErr("Серий позади при нехватке места", "нужно целое число от 0")
			}
			n.KeepBehind = *s.KeepBehind
		}
		if s.MinFreeGB != nil {
			if *s.MinFreeGB < 0 {
				return v, fieldErr("Запас места, ГБ", "нужно целое число от 0")
			}
			n.MinFreeGB = *s.MinFreeGB
		}
		if s.UploadLimitMBps.Set {
			if f := s.UploadLimitMBps.Value; f != nil && (*f < 0 || math.IsNaN(*f) || math.IsInf(*f, 0)) {
				return v, fieldErr("Раздача, МБ/с", "нужно число от 0 или пустое поле")
			}
			n.UploadMBps = s.UploadLimitMBps.Value
		}
	}
	if p.Player != nil {
		if !slices.Contains(Players, *p.Player) {
			return v, fieldErr("Плеер", "нужно auto, vlc или mpc-hc")
		}
		n.Player = *p.Player
	}
	if c := p.Catalog; c != nil && c.PreferredFormat != nil {
		if !slices.Contains(Formats, *c.PreferredFormat) {
			return v, fieldErr("Формат в приоритете", "нужно «нет», MKV, MP4 или AVI")
		}
		n.PreferredFormat = *c.PreferredFormat
	}
	if i := p.IPTV; i != nil {
		if err := i.apply(&n); err != nil {
			return v, err
		}
	}
	if c := p.Catalog; c != nil && c.Sections != nil {
		s, err := joinSections(c.Sections)
		if err != nil {
			return v, err
		}
		n.Sections = s
	}
	return n, nil
}

// setAddress — адрес сайта из поля пульта: нет поля — не менять, пусто — стереть, иначе —
// приведённый к «схема://хост» (source.SiteAddress).
func setAddress(in, dst *string, field string) error {
	if in == nil {
		return nil
	}
	if strings.TrimSpace(*in) == "" {
		*dst = ""
		return nil
	}
	a, err := source.SiteAddress(*in)
	if err != nil {
		return fieldErr(field, "%v", err)
	}
	*dst = a
	return nil
}

// searchAddress — адрес источника поиска: пусто — выключен; свой Jackett или Jacred в домашней сети без
// схемы — http:// (у них обычно нет HTTPS), «localhost» — 127.0.0.1; дальше — как адрес сайта.
func searchAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		host, _, _ := strings.Cut(s, "/")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if netx.PrivateHost(host) {
			s = "http://" + s
		}
	}
	if u, err := url.Parse(s); err == nil && strings.EqualFold(u.Hostname(), "localhost") {
		u.Host = strings.Replace(strings.ToLower(u.Host), "localhost", "127.0.0.1", 1)
		s = u.String()
	}
	return source.SiteAddress(s)
}

// composeProxy — адрес прокси из полей пульта. Поля, которых нет в запросе, берутся из прежнего
// адреса; пароль без поля — прежний, "" — стереть.
func composeProxy(old string, p ProxyPatch) (string, error) {
	cur := splitProxy(old)
	pw := cur.password
	typ, addr, login := cur.Type, cur.Address, cur.Login
	if p.Type != nil {
		typ = *p.Type
	}
	if p.Address != nil {
		addr = strings.TrimSpace(*p.Address)
	}
	if p.Login != nil {
		login = strings.TrimSpace(*p.Login)
	}
	if p.Password != nil {
		pw = *p.Password
	}
	switch typ {
	case "none":
		return "", nil
	case "http", "socks5":
	default:
		return "", fieldErr("Прокси", "тип — «Нет», HTTP или SOCKS5")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return "", fieldErr("Прокси", "адрес — «хост:порт», например 192.168.1.20:3128")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fieldErr("Прокси", "порт — число от 1 до 65535")
	}
	if pw != "" && login == "" {
		return "", fieldErr("Прокси", "пароль без логина")
	}
	u := url.URL{Scheme: typ, Host: addr}
	switch {
	case login != "" && pw != "":
		u.User = url.UserPassword(login, pw)
	case login != "":
		u.User = url.User(login)
	}
	if _, err := netx.ParseProxy(u.String()); err != nil {
		return "", fieldErr("Прокси", "%v", err)
	}
	return u.String(), nil
}

// proxyParts — адрес прокси, разобранный на поля пульта.
type proxyParts struct {
	Type, Address, Login string
	password             string
}

func splitProxy(s string) proxyParts {
	u, err := url.Parse(s)
	if s == "" || err != nil || u.Host == "" {
		return proxyParts{Type: "none"}
	}
	p := proxyParts{Type: u.Scheme, Address: u.Host}
	if u.User != nil {
		p.Login = u.User.Username()
		p.password, _ = u.User.Password()
	}
	return p
}

// splitSections — строка разделов по трекерам: {"rutracker": ["2110", "46+"], "rutor": ["12"]}.
func splitSections(s string) map[string][]string {
	out := map[string][]string{}
	for _, part := range strings.Split(s, ",") {
		tracker, id, ok := strings.Cut(strings.TrimSpace(part), ":")
		if ok && tracker != "" && id != "" {
			out[tracker] = append(out[tracker], id)
		}
	}
	return out
}

// joinSections — обратно в строку: сначала Rutracker, потом Rutor, остальные по имени. Проверка
// самих номеров — у каталога (Applier.Check): он знает дерево разделов.
func joinSections(m map[string][]string) (string, error) {
	trackers := make([]string, 0, len(m))
	for t := range m {
		trackers = append(trackers, t)
	}
	order := func(t string) int { return cmp.Or(slices.Index([]string{"rutracker", "rutor"}, t)+1, 3) }
	slices.SortFunc(trackers, func(a, b string) int { return cmp.Or(cmp.Compare(order(a), order(b)), cmp.Compare(a, b)) })
	var parts []string
	for _, t := range trackers {
		for _, id := range m[t] {
			id = strings.TrimSpace(id)
			if id == "" || strings.ContainsAny(id, ",: ") {
				return "", fieldErr("Разделы каталога", "раздел %q у %s не понят", id, t)
			}
			parts = append(parts, t+":"+id)
		}
	}
	if len(parts) == 0 {
		return "", fieldErr("Разделы каталога", "выберите хотя бы один раздел")
	}
	return strings.Join(parts, ","), nil
}

var (
	reCountry  = regexp.MustCompile(`^[A-Z]{2}$`)
	reLanguage = regexp.MustCompile(`^[a-z]{3}$`)
)

// apply — поля каналов: проверка и перенос в n.
func (i *IPTVPatch) apply(n *Values) error {
	if i.EPGURL != nil {
		u := strings.TrimSpace(*i.EPGURL)
		if pu, err := url.Parse(u); u != "" && (err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "") {
			return fieldErr("Телепрограмма", "нужна ссылка http(s) или пустое поле")
		}
		n.EPGURL = u
	}
	if i.HiddenCategories != nil {
		for _, c := range *i.HiddenCategories {
			if !slices.Contains(labels.CategoryOrder, c) {
				return fieldErr("Скрытые категории", "неизвестная категория %q", c)
			}
		}
		n.HiddenCategories = clean(*i.HiddenCategories)
	}
	if i.HiddenCountries != nil {
		for _, c := range *i.HiddenCountries {
			if c != "" && !reCountry.MatchString(c) {
				return fieldErr("Скрытые страны", "страна — код из двух заглавных латинских букв, например UA")
			}
		}
		n.HiddenCountries = clean(*i.HiddenCountries)
	}
	if i.HiddenLanguages != nil {
		for _, l := range *i.HiddenLanguages {
			if l != "" && !reLanguage.MatchString(l) {
				return fieldErr("Скрытые языки", "язык — код из трёх строчных латинских букв, например ara")
			}
		}
		n.HiddenLanguages = clean(*i.HiddenLanguages)
	}
	if i.HideOtherZones != nil {
		n.HideOtherZones = *i.HideOtherZones
	}
	if i.UTCOffset != nil {
		if *i.UTCOffset < -12 || *i.UTCOffset > 14 {
			return fieldErr("Часовой пояс каналов", "нужно целое число часов от −12 до 14")
		}
		n.UTCOffset = *i.UTCOffset
	}
	return nil
}

// clean — без повторов, по порядку.
func clean(l []string) []string {
	out := []string{}
	for _, v := range l {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
