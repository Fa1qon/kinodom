package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"kinodom/internal/httpx"
	"kinodom/internal/source"
	"kinodom/internal/store"
	"kinodom/internal/trackerpack"
)

// View — настройки для пульта. Секретов в ответе нет: только признак «задан».
type View struct {
	Trackers  []TrackerView `json:"trackers"` // адреса, которые ввёл человек, с движком из профиля
	t2 t2View `json:"t2"`
	t1     t1View     `json:"t1"`
	Setup     SetupView     `json:"setup"`
	Proxy     ProxyView     `json:"proxy"`
	Kinopoisk KinopoiskView `json:"kinopoisk"`
	Search    SearchView    `json:"search"`
	Storage   StorageView   `json:"storage"`
	Player    string        `json:"player"`
	Autorun   bool          `json:"autorun"` // запуск Kinodom при старте ОС
	Catalog   CatalogView   `json:"catalog"`
	IPTV      IPTVView      `json:"iptv"`
}

// TrackerView — трекер из списка «Трекеры». Название — из профиля, найденного по адресу: программа сама
// трекеров не знает (docs/data-packs-and-updates.md, раздел 3).
type TrackerView struct {
	Address string `json:"address"`
	Name    string `json:"name"`   // "" — профиль по адресу больше не находится
	Engine  string `json:"engine"` // t1 | t2 | cardigann
}

type t2View struct {
	Login       string `json:"login"`
	PasswordSet bool   `json:"passwordSet"`
	Address     string `json:"address"`     // "" — t2 выключен
	APIAddress  string `json:"apiAddress"`  // "" — по правилу из адреса сайта
	FeedAddress string `json:"feedAddress"` // "" — по правилу из адреса сайта
}

type t1View struct {
	Address         string `json:"address"`         // "" — t1 выключен
	DownloadAddress string `json:"downloadAddress"` // "" — по правилу из адреса сайта
}

type SetupView struct {
	Done bool `json:"done"` // мастер начальных настроек пройден
}

type ProxyView struct {
	Type        string `json:"type"` // none, http, socks5
	Address     string `json:"address"`
	Login       string `json:"login"`
	PasswordSet bool   `json:"passwordSet"`
}

type KinopoiskView struct {
	KeySet bool `json:"keySet"`
}

// SearchView — источник поиска Jacred / Jackett: ключ — только признак «задан».
type SearchView struct {
	Address string `json:"address"` // "" — выключен
	KeySet  bool   `json:"keySet"`
}

type StorageView struct {
	DownloadsDir    string   `json:"downloadsDir"`
	KeepDays        int      `json:"keepDays"`
	KeepBehind      int      `json:"keepBehind"` // серий позади просмотренной оставлять при нехватке места
	MinFreeGB       int      `json:"minFreeGB"`
	UploadLimitMBps *float64 `json:"uploadLimitMBps"` // null — без ограничения; 0 — не раздавать
}

type CatalogView struct {
	Sections        map[string][]string `json:"sections"`
	PreferredFormat string              `json:"preferredFormat"` // "" — нет
	Order           string              `json:"order"`           // порядок разделов по умолчанию
}

// IPTVView — настройки каналов (спека этапа 8, раздел 5.6).
type IPTVView struct {
	EPGURL           string   `json:"epgUrl"` // "" — по умолчанию
	HiddenCategories []string `json:"hiddenCategories"`
	HiddenCountries  []string `json:"hiddenCountries"`
	HiddenLanguages  []string `json:"hiddenLanguages"`
	UTCOffset        int      `json:"utcOffset"`
}

// View — настройки для пульта.
func (v Values) View() View {
	p := splitProxy(v.Proxy)
	trackers := []TrackerView{}
	if v.t2Address != "" {
		trackers = append(trackers, TrackerView{Address: v.t2Address, Engine: "t2"})
	}
	if v.t1Address != "" {
		trackers = append(trackers, TrackerView{Address: v.t1Address, Engine: "t1"})
	}
	for _, t := range v.OpenTrackers {
		trackers = append(trackers, TrackerView{Address: t.Address, Engine: "cardigann"})
	}
	return View{
		Trackers: trackers,
		t2: t2View{Login: v.t2Login, PasswordSet: v.t2Password != "",
			Address: v.t2Address, APIAddress: v.t2API, FeedAddress: v.t2Feed},
		t1:     t1View{Address: v.t1Address, DownloadAddress: v.t1Download},
		Setup:     SetupView{Done: v.SetupDone},
		Proxy:     ProxyView{Type: p.Type, Address: p.Address, Login: p.Login, PasswordSet: p.password != ""},
		Kinopoisk: KinopoiskView{KeySet: v.KinopoiskKey != ""},
		Search:    SearchView{Address: v.SearchAddress, KeySet: v.SearchKey != ""},
		Storage:   StorageView{DownloadsDir: v.DownloadsDir, KeepDays: v.KeepDays, KeepBehind: v.KeepBehind, MinFreeGB: v.MinFreeGB, UploadLimitMBps: v.UploadMBps},
		Player:    v.Player,
		Autorun:   v.Autorun,
		Catalog:   CatalogView{Sections: splitSections(v.Sections), PreferredFormat: v.PreferredFormat, Order: v.CatalogOrder},
		IPTV: IPTVView{EPGURL: v.EPGURL, HiddenCategories: nonNil(v.HiddenCategories), HiddenCountries: nonNil(v.HiddenCountries),
			HiddenLanguages: nonNil(v.HiddenLanguages), UTCOffset: v.UTCOffset},
	}
}

// Patch — изменения из пульта: только присланные поля. Секрет: поля нет — не меняется, "" — стереть.
type Patch struct {
	// Trackers — весь список адресов трекеров: движок каждого — из профиля по адресу; адреса t2 и
	// t1 (если есть) записываются в их поля, остальные — в OpenTrackers. Прочие поля — после него.
	Trackers  *[]string       `json:"trackers"`
	open      *[]OpenTracker  // разобранные Trackers
	t2 *t2Patch `json:"t2"`
	t1     *t1Patch     `json:"t1"`
	Setup     *SetupPatch     `json:"setup"`
	Proxy     *ProxyPatch     `json:"proxy"`
	Kinopoisk *KinopoiskPatch `json:"kinopoisk"`
	Search    *SearchPatch    `json:"search"`
	Storage   *StoragePatch   `json:"storage"`
	Player    *string         `json:"player"`
	Autorun   *bool           `json:"autorun"`
	Catalog   *CatalogPatch   `json:"catalog"`
	IPTV      *IPTVPatch      `json:"iptv"`
}

type t2Patch struct {
	Login       *string `json:"login"`
	Password    *string `json:"password"`
	Address     *string `json:"address"`
	APIAddress  *string `json:"apiAddress"`
	FeedAddress *string `json:"feedAddress"`
}

type t1Patch struct {
	Address         *string `json:"address"`
	DownloadAddress *string `json:"downloadAddress"`
}

type SetupPatch struct {
	Done *bool `json:"done"`
}

type ProxyPatch struct {
	Type     *string `json:"type"`
	Address  *string `json:"address"`
	Login    *string `json:"login"`
	Password *string `json:"password"`
}

type KinopoiskPatch struct {
	Key *string `json:"key"`
}

// SearchPatch — адрес ("" — выключить) и ключ ("" — стереть) источника поиска.
type SearchPatch struct {
	Address *string `json:"address"`
	Key     *string `json:"key"`
}

type StoragePatch struct {
	DownloadsDir    *string  `json:"downloadsDir"`
	KeepDays        *int     `json:"keepDays"`
	KeepBehind      *int     `json:"keepBehind"`
	MinFreeGB       *int     `json:"minFreeGB"`
	UploadLimitMBps Optional `json:"uploadLimitMBps"`
}

type CatalogPatch struct {
	Sections        map[string][]string `json:"sections"`
	PreferredFormat *string             `json:"preferredFormat"`
	Order           *string             `json:"order"`
}

// IPTVPatch — изменения настроек каналов.
type IPTVPatch struct {
	EPGURL           *string   `json:"epgUrl"`
	HiddenCategories *[]string `json:"hiddenCategories"`
	HiddenCountries  *[]string `json:"hiddenCountries"`
	HiddenLanguages  *[]string `json:"hiddenLanguages"`
	UTCOffset        *int      `json:"utcOffset"`
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// Optional — число, null или «поля нет в запросе». Отличить null от отсутствия обычный указатель
// не даёт: у лимита отдачи null — «без ограничения», а отсутствие — «не менять».
type Optional struct {
	Set   bool
	Value *float64
}

// UnmarshalJSON вызывается и для null — так пакет encoding/json устроен для таких типов.
func (o *Optional) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	o.Value = &f
	return nil
}

// Applier — применение настроек к модулям. Реализует приложение: только оно знает все модули.
type Applier interface {
	// Check — проверки, которым нужны модули (папка загрузок, разделы по дереву трекера).
	// Ошибка — отказ: ничего не сохраняется.
	Check(ctx context.Context, old, new Values) error
	// Apply — применить уже сохранённые настройки к работающим модулям.
	Apply(ctx context.Context, old, new Values)
}

// Profiles — профили трекеров по адресу (trackers.pack).
type Profiles interface {
	Lookup(addr string) (trackerpack.Profile, error)
}

// Service — настройки работающего сервера.
type Service struct {
	db       *store.DB
	apply    Applier
	profiles Profiles // nil — список трекеров не меняется

	mu  sync.Mutex
	cur Values
}

func New(db *store.DB, cur Values, a Applier) *Service {
	return &Service{db: db, cur: cur, apply: a}
}

// SetProfiles — профили трекеров (приложение загружает их после настроек и подменяет при обновлении базы).
func (s *Service) SetProfiles(p Profiles) {
	s.mu.Lock()
	s.profiles = p
	s.mu.Unlock()
}

// View — настройки для пульта с названиями трекеров из профилей.
func (s *Service) View() View {
	s.mu.Lock()
	v, pr := s.cur.View(), s.profiles
	s.mu.Unlock()
	for i, t := range v.Trackers {
		if pr != nil {
			if p, err := pr.Lookup(t.Address); err == nil {
				v.Trackers[i].Name = p.Name
			}
		}
	}
	return v
}

// resolveTrackers — список адресов в поля настроек по профилям: каждый адрес должен найтись в профилях,
// один трекер — один адрес.
func resolveTrackers(p *Patch, pr Profiles) error {
	if p.Trackers == nil {
		return nil
	}
	if pr == nil {
		return fieldErr("Трекеры", "профили трекеров ещё не загружены — попробуйте через минуту")
	}
	t1, t2 := "", ""
	open := []OpenTracker{}
	seen := map[string]bool{}
	for _, raw := range *p.Trackers {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		field := "Трекер " + raw
		addr, err := source.SiteAddress(raw)
		if err != nil {
			return fieldErr(field, "%v", err)
		}
		prof, err := pr.Lookup(addr)
		if err != nil {
			return fieldErr(field, "%v", err)
		}
		if seen[prof.Name] {
			return fieldErr(field, "%s уже в списке — оставьте один адрес", prof.Name)
		}
		seen[prof.Name] = true
		switch prof.Engine {
		case "t1":
			t1 = addr
		case "t2":
			t2 = addr
		case "cardigann":
			open = append(open, OpenTracker{Def: prof.Def, Address: addr})
		default:
			return fieldErr(field, "нужна новая версия Kinodom")
		}
	}
	if p.t1 == nil {
		p.t1 = &t1Patch{}
	}
	if p.t2 == nil {
		p.t2 = &t2Patch{}
	}
	p.t1.Address, p.t2.Address, p.open = &t1, &t2, &open
	return nil
}

// Current — действующие настройки.
func (s *Service) Current() Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Update проверяет изменения, сохраняет изменившееся и применяет к модулям. Изменения идут по
// одному: две вкладки пульта не перезапишут друг другу половину настроек.
func (s *Service) Update(ctx context.Context, p Patch) (Values, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.cur
	if err := resolveTrackers(&p, s.profiles); err != nil {
		return old, err
	}
	n, err := old.With(p)
	if err != nil {
		return old, err
	}
	if s.apply != nil {
		if err := s.apply.Check(ctx, old, n); err != nil {
			return old, err
		}
	}
	if err := save(ctx, s.db, old, n); err != nil {
		return old, err
	}
	s.cur = n
	if s.apply != nil {
		s.apply.Apply(context.WithoutCancel(ctx), old, n)
	}
	return n, nil
}

// Router — то, что пакету нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
}

// Register — GET с любого устройства (телевизору и `kinodom open` нужен плеер), PUT — из домашней
// сети (спека этапа 7, раздел 10.1).
func (s *Service) Register(r Router) {
	r.Handle("GET /api/v1/settings", "", http.HandlerFunc(s.handleGet))
	r.HandleHome("PUT /api/v1/settings", "", http.HandlerFunc(s.handlePut))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.View())
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if !httpx.ReadJSON(w, r, &p) {
		return
	}
	_, err := s.Update(r.Context(), p)
	var fe *FieldError
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, s.View())
	case errors.As(err, &fe):
		httpx.WriteError(w, http.StatusBadRequest, fe.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
