package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"kinodom/internal/httpx"
	"kinodom/internal/store"
)

// View — настройки для пульта. Секретов в ответе нет: только признак «задан».
type View struct {
	Rutracker RutrackerView `json:"rutracker"`
	Rutor     RutorView     `json:"rutor"`
	Setup     SetupView     `json:"setup"`
	Proxy     ProxyView     `json:"proxy"`
	Kinopoisk KinopoiskView `json:"kinopoisk"`
	Storage   StorageView   `json:"storage"`
	Player    string        `json:"player"`
	Catalog   CatalogView   `json:"catalog"`
	IPTV      IPTVView      `json:"iptv"`
}

type RutrackerView struct {
	Login       string `json:"login"`
	PasswordSet bool   `json:"passwordSet"`
	Address     string `json:"address"`     // "" — Rutracker выключен
	APIAddress  string `json:"apiAddress"`  // "" — по правилу из адреса сайта
	FeedAddress string `json:"feedAddress"` // "" — по правилу из адреса сайта
}

type RutorView struct {
	Address         string `json:"address"`         // "" — Rutor выключен
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
}

// IPTVView — настройки каналов (спека этапа 8, раздел 5.6).
type IPTVView struct {
	EPGURL           string   `json:"epgUrl"` // "" — по умолчанию
	HiddenCategories []string `json:"hiddenCategories"`
	HiddenCountries  []string `json:"hiddenCountries"`
	HiddenLanguages  []string `json:"hiddenLanguages"`
	HideOtherZones   bool     `json:"hideOtherZones"`
	UTCOffset        int      `json:"utcOffset"`
}

// View — настройки для пульта.
func (v Values) View() View {
	p := splitProxy(v.Proxy)
	return View{
		Rutracker: RutrackerView{Login: v.RutrackerLogin, PasswordSet: v.RutrackerPassword != "",
			Address: v.RutrackerAddress, APIAddress: v.RutrackerAPI, FeedAddress: v.RutrackerFeed},
		Rutor:     RutorView{Address: v.RutorAddress, DownloadAddress: v.RutorDownload},
		Setup:     SetupView{Done: v.SetupDone},
		Proxy:     ProxyView{Type: p.Type, Address: p.Address, Login: p.Login, PasswordSet: p.password != ""},
		Kinopoisk: KinopoiskView{KeySet: v.KinopoiskKey != ""},
		Storage:   StorageView{DownloadsDir: v.DownloadsDir, KeepDays: v.KeepDays, KeepBehind: v.KeepBehind, MinFreeGB: v.MinFreeGB, UploadLimitMBps: v.UploadMBps},
		Player:    v.Player,
		Catalog:   CatalogView{Sections: splitSections(v.Sections), PreferredFormat: v.PreferredFormat},
		IPTV: IPTVView{EPGURL: v.EPGURL, HiddenCategories: nonNil(v.HiddenCategories), HiddenCountries: nonNil(v.HiddenCountries),
			HiddenLanguages: nonNil(v.HiddenLanguages), HideOtherZones: v.HideOtherZones, UTCOffset: v.UTCOffset},
	}
}

// Patch — изменения из пульта: только присланные поля. Секрет: поля нет — не меняется, "" — стереть.
type Patch struct {
	Rutracker *RutrackerPatch `json:"rutracker"`
	Rutor     *RutorPatch     `json:"rutor"`
	Setup     *SetupPatch     `json:"setup"`
	Proxy     *ProxyPatch     `json:"proxy"`
	Kinopoisk *KinopoiskPatch `json:"kinopoisk"`
	Storage   *StoragePatch   `json:"storage"`
	Player    *string         `json:"player"`
	Catalog   *CatalogPatch   `json:"catalog"`
	IPTV      *IPTVPatch      `json:"iptv"`
}

type RutrackerPatch struct {
	Login       *string `json:"login"`
	Password    *string `json:"password"`
	Address     *string `json:"address"`
	APIAddress  *string `json:"apiAddress"`
	FeedAddress *string `json:"feedAddress"`
}

type RutorPatch struct {
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
}

// IPTVPatch — изменения настроек каналов.
type IPTVPatch struct {
	EPGURL           *string   `json:"epgUrl"`
	HiddenCategories *[]string `json:"hiddenCategories"`
	HiddenCountries  *[]string `json:"hiddenCountries"`
	HiddenLanguages  *[]string `json:"hiddenLanguages"`
	HideOtherZones   *bool     `json:"hideOtherZones"`
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

// Service — настройки работающего сервера.
type Service struct {
	db    *store.DB
	apply Applier

	mu  sync.Mutex
	cur Values
}

func New(db *store.DB, cur Values, a Applier) *Service {
	return &Service{db: db, cur: cur, apply: a}
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
	httpx.WriteJSON(w, http.StatusOK, s.Current().View())
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if !httpx.ReadJSON(w, r, &p) {
		return
	}
	v, err := s.Update(r.Context(), p)
	var fe *FieldError
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, v.View())
	case errors.As(err, &fe):
		httpx.WriteError(w, http.StatusBadRequest, fe.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
