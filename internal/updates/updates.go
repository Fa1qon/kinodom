// Package updates — базы и программа из релизов GitHub без переустановки (docs/data-packs-and-updates.md).
//
// Базы (каталог и граф похожих, профили трекеров) выходят релизами data-ГГГГ.ММ.ДД, программа — v*. В каждом
// релизе manifest.json и его подпись Ed25519 (manifest.json.sig): без верной подписи и хэша файл не ставится,
// даже если релиз подменят. Сервер раз в сутки смотрит список релизов, сам ставит новые базы (если не
// выключено) и сообщает о новой версии программы.
package updates

import (
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PublicKey — открытый ключ подписи релизов (закрытый — только у владельца, KINODOM_SIGN_KEY).
const PublicKey = "zGL+Q79MckZ9JTeqTRXDxkzAhy137o/DlFI1oVz52bc="

const (
	DefaultRepo = "Fa1qon/kinodom"
	DefaultAPI  = "https://api.github.com"
	DataPrefix  = "data-"
	checkEvery  = 24 * time.Hour
	retryEvery  = time.Hour
	maxManifest = 1 << 20
)

// Manifest — что лежит в релизе.
type Manifest struct {
	Version string `json:"version"` // «2026.11.09» у баз, «0.17.0» у программы
	Files   []File `json:"files"`
}

// File — файл релиза: база (Pack) или установщик (Pack == "app").
type File struct {
	Name   string `json:"name"`   // имя в релизе; .gz — сжат, ставится распакованным
	Pack   string `json:"pack"`   // recs | trackers | app
	Schema int    `json:"schema"` // номер схемы базы: программа берёт только понятную
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // хэш файла как он лежит в релизе
}

// Pack — база, которую программа умеет ставить.
type Pack struct {
	Name   string
	Schema int
	// Install ставит проверенный распакованный файл path (его можно переместить). Своя база новее — nil
	// без замены: установщик программы мог принести более свежую.
	Install func(ctx context.Context, path string) error
}

// Store — где помнить состояние (настройки программы).
type Store interface {
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

type Options struct {
	Repo    string // "" — DefaultRepo
	API     string // "" — DefaultAPI (тесты — свой сервер)
	Key     ed25519.PublicKey
	Client  *http.Client
	Dir     string // временные файлы загрузки
	Store   Store
	Packs   []Pack
	Version string // версия программы
	Log     *slog.Logger
}

// Status — для экрана «Обновления».
type Status struct {
	Auto       bool      `json:"auto"`
	CheckedAt  time.Time `json:"checkedAt,omitzero"`
	Error      string    `json:"error,omitempty"`
	Data       string    `json:"data,omitempty"`       // поставленный выпуск баз
	DataNew    string    `json:"dataNew,omitempty"`    // вышел новее, ещё не поставлен (автообновление выключено)
	Version    string    `json:"version"`              // программа
	AppNew     string    `json:"appNew,omitempty"`     // вышла новее
	AppURL     string    `json:"appUrl,omitempty"`     // страница релиза
	AppSigned  bool      `json:"appSigned,omitempty"`  // в релизе подписанный установщик: «Обновить» ставит сам
	Installing bool      `json:"installing,omitempty"` // идёт загрузка баз
}

type Service struct {
	o   Options
	mu  sync.Mutex
	st  Status
	app *release   // новая версия программы из последней проверки
	run sync.Mutex // одна проверка за раз
}

const (
	keyAuto = "updates.auto" // "0" — базы не ставить самим
	keyData = "updates.data" // поставленный выпуск баз
)

func New(o Options) *Service {
	if o.Repo == "" {
		o.Repo = DefaultRepo
	}
	if o.API == "" {
		o.API = DefaultAPI
	}
	if o.Key == nil {
		o.Key, _ = base64.StdEncoding.DecodeString(PublicKey)
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o, st: Status{Version: o.Version}}
}

// Status — последнее известное состояние.
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := s.st
	s.mu.Unlock()
	st.Auto = s.auto(ctx)
	st.Data, _, _ = s.o.Store.Setting(ctx, keyData)
	if st.DataNew != "" && st.DataNew <= st.Data {
		st.DataNew = ""
	}
	return st
}

func (s *Service) auto(ctx context.Context) bool {
	v, _, _ := s.o.Store.Setting(ctx, keyAuto)
	return v != "0"
}

// SetAuto — ставить ли базы самим.
func (s *Service) SetAuto(ctx context.Context, on bool) error {
	v := "1"
	if !on {
		v = "0"
	}
	return s.o.Store.SetSetting(ctx, keyAuto, v)
}

// Run — проверка раз в сутки (после ошибки — через час), пока жив ctx.
func (s *Service) Run(ctx context.Context) {
	for {
		wait := checkEvery
		if err := s.Check(ctx, s.auto(ctx)); err != nil {
			s.o.Log.Warn("обновления: проверка не удалась", "err", err)
			wait = retryEvery
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// release — релиз GitHub (только нужные поля).
type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	URL        string  `json:"html_url"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (r release) asset(name string) (asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return asset{}, false
}

// Check смотрит релизы; install — сразу поставить новые базы.
func (s *Service) Check(ctx context.Context, install bool) error {
	s.run.Lock()
	defer s.run.Unlock()
	err := s.check(ctx, install)
	s.mu.Lock()
	s.st.CheckedAt = time.Now()
	s.st.Error = ""
	if err != nil {
		s.st.Error = err.Error()
	}
	s.mu.Unlock()
	return err
}

func (s *Service) check(ctx context.Context, install bool) error {
	var rels []release
	if err := s.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=30", s.o.API, s.o.Repo), &rels); err != nil {
		return fmt.Errorf("список релизов: %w", err)
	}
	var data, app *release
	for i := range rels {
		r := &rels[i]
		if r.Draft {
			continue
		}
		switch {
		case strings.HasPrefix(r.Tag, DataPrefix):
			if data == nil || r.Tag > data.Tag {
				data = r
			}
		case strings.HasPrefix(r.Tag, "v") && !r.Prerelease:
			if app == nil || newer(r.Tag, app.Tag) {
				app = r
			}
		}
	}
	s.mu.Lock()
	s.st.AppNew, s.st.AppURL, s.st.AppSigned, s.app = "", "", false, nil
	if app != nil && s.o.Version != "dev" && newer(app.Tag, s.o.Version) { // сборка разработчика не обновляется
		_, signed := app.asset("manifest.json.sig")
		s.st.AppNew, s.st.AppURL, s.st.AppSigned, s.app = strings.TrimPrefix(app.Tag, "v"), app.URL, signed && canInstall, app
	}
	s.mu.Unlock()
	if data == nil {
		return nil
	}
	have, _, _ := s.o.Store.Setting(ctx, keyData)
	ver := strings.TrimPrefix(data.Tag, DataPrefix)
	if ver <= have {
		return nil
	}
	s.mu.Lock()
	s.st.DataNew = ver
	s.mu.Unlock()
	if !install {
		return nil
	}
	return s.installData(ctx, *data, ver)
}

// installData ставит базы релиза, которые программа понимает. Выпуск записывается поставленным, только
// если все понятные базы встали: иначе завтра попробуем снова.
func (s *Service) installData(ctx context.Context, r release, ver string) error {
	s.setInstalling(true)
	defer s.setInstalling(false)
	m, err := s.manifest(ctx, r)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range m.Files {
		p := s.pack(f)
		if p == nil {
			continue
		}
		if err := s.installFile(ctx, r, f, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Name, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	s.o.Log.Info("обновления: базы поставлены", "release", ver)
	return s.o.Store.SetSetting(ctx, keyData, ver)
}

func (s *Service) setInstalling(on bool) {
	s.mu.Lock()
	s.st.Installing = on
	s.mu.Unlock()
}

func (s *Service) pack(f File) *Pack {
	for i := range s.o.Packs {
		if p := &s.o.Packs[i]; p.Name == f.Pack && p.Schema == f.Schema {
			return p
		}
	}
	return nil
}

// manifest — манифест релиза с проверенной подписью.
func (s *Service) manifest(ctx context.Context, r release) (Manifest, error) {
	var m Manifest
	ma, ok1 := r.asset("manifest.json")
	sa, ok2 := r.asset("manifest.json.sig")
	if !ok1 || !ok2 {
		return m, errors.New("в релизе нет манифеста или подписи")
	}
	body, err := s.getBytes(ctx, ma.URL, maxManifest)
	if err != nil {
		return m, err
	}
	sig, err := s.getBytes(ctx, sa.URL, 1024)
	if err != nil {
		return m, err
	}
	if err := Verify(s.o.Key, body, sig); err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("манифест: %w", err)
	}
	return m, nil
}

// Verify проверяет подпись (base64 Ed25519) содержимого.
func Verify(key ed25519.PublicKey, body, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, body, raw) {
		return errors.New("подпись манифеста неверна")
	}
	return nil
}

// installFile качает файл, сверяет размер и хэш, распаковывает и отдаёт базе.
func (s *Service) installFile(ctx context.Context, r release, f File, p *Pack) error {
	a, ok := r.asset(f.Name)
	if !ok {
		return errors.New("файла нет в релизе")
	}
	if err := os.MkdirAll(s.o.Dir, 0o755); err != nil {
		return err
	}
	dl := filepath.Join(s.o.Dir, f.Name+".part")
	defer os.Remove(dl)
	if err := s.download(ctx, a.URL, dl, f); err != nil {
		return err
	}
	path := dl
	if strings.HasSuffix(f.Name, ".gz") {
		path = filepath.Join(s.o.Dir, strings.TrimSuffix(f.Name, ".gz"))
		defer os.Remove(path)
		if err := gunzip(dl, path); err != nil {
			return err
		}
	}
	return p.Install(ctx, path)
}

func (s *Service) download(ctx context.Context, url, dst string, f File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.o.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("загрузка: %s", resp.Status)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != f.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256) {
		return errors.New("файл не совпал с манифестом")
	}
	return nil
}

func gunzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (s *Service) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kinodom/"+s.o.Version)
	resp, err := s.o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

func (s *Service) getBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := s.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (s *Service) getJSON(ctx context.Context, url string, v any) error {
	b, err := s.getBytes(ctx, url, 4<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// newer — версия a новее b («v0.17.0» > «0.16.28»; хвост сборки «-26-gabc» не считается).
func newer(a, b string) bool {
	pa, pb := nums(a), nums(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func nums(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// ErrNoApp — новой подписанной версии программы нет (или проверки ещё не было).
var ErrNoApp = errors.New("новой версии нет")

// InstallApp качает установщик новой версии, сверяет подпись и хэш и запускает его тихо: установщик
// остановит службу, поставит версию и запустит её снова; приложения на ТВ подтянут новый APK как обычно.
func (s *Service) InstallApp(ctx context.Context) error {
	s.run.Lock()
	defer s.run.Unlock()
	s.mu.Lock()
	r := s.app
	s.mu.Unlock()
	if r == nil || !canInstall {
		return ErrNoApp
	}
	m, err := s.manifest(ctx, *r)
	if err != nil {
		return err
	}
	for _, f := range m.Files {
		if f.Pack != "app" {
			continue
		}
		a, ok := r.asset(f.Name)
		if !ok {
			return errors.New("установщика нет в релизе")
		}
		if err := os.MkdirAll(s.o.Dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(s.o.Dir, filepath.Base(f.Name))
		if err := s.download(ctx, a.URL, path, f); err != nil {
			os.Remove(path)
			return err
		}
		s.o.Log.Info("обновления: запускаю установщик", "version", m.Version)
		return start(path)
	}
	return ErrNoApp
}

// start — запуск установщика (тесты подменяют).
var start = launch
