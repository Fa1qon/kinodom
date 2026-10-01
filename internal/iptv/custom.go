package iptv

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
)

// Свой канал (план 14Д): «Новый канал» из «Не распознано» — название, логотип (адрес картинки или файл),
// категория и страна; ключ «my-N», телепрограммы у него нет. Потоки группы привязываются к нему правилом
// названия, категория и страна — правкой канала.

// Custom — свой канал.
type Custom struct {
	Key  string
	Name string
	Logo string // адрес картинки; «upload:<ключ>» — загруженный файл; "" — нет
}

// CustomInput — «Новый канал» из пульта.
type CustomInput struct {
	Name     string `json:"name"`
	Logo     string `json:"logo"`     // адрес картинки http(s); "" — нет или файл
	LogoData []byte `json:"logoData"` // загруженный файл (base64 в JSON)
	Category string `json:"category"` // labels.CategoryOrder; "" — не задана
	Country  string `json:"country"`  // код страны («RU»); "" — не задана
	Group    string `json:"group"`    // нераспознанное название, чьи потоки — к новому каналу; "" — без потоков
}

const maxLogo = 1 << 20

// customPrefix — ключи своих каналов.
const customPrefix = "my-"

func (m *Module) customLogoPath(key string) string {
	return filepath.Join(m.o.Dir, "custom-logos", key)
}

// CreateCustom — новый свой канал; ключ — «my-N».
func (m *Module) CreateCustom(ctx context.Context, in CustomInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", &FieldError{"нужно название канала (до 100 знаков)"}
	}
	logo := strings.TrimSpace(in.Logo)
	if logo != "" && !strings.HasPrefix(logo, "https://") && !strings.HasPrefix(logo, "http://") {
		return "", &FieldError{"логотип — адрес картинки (http или https) или файл"}
	}
	if len(in.LogoData) > 0 {
		if len(in.LogoData) > maxLogo {
			return "", &FieldError{"логотип больше 1 МБ"}
		}
		if !strings.HasPrefix(http.DetectContentType(in.LogoData), "image/") {
			return "", &FieldError{"логотип — не картинка"}
		}
	}
	if in.Category != "" && !slices.Contains(labels.CategoryOrder, in.Category) {
		return "", &FieldError{"неизвестная категория"}
	}
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	if country != "" && (len(country) != 2 || strings.Trim(country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "") {
		return "", &FieldError{"страна — двухбуквенный код (RU)"}
	}
	group := m3u.Norm(in.Group)
	m.plMu.Lock()
	defer m.plMu.Unlock()
	n, err := m.d.nextCustom(ctx)
	if err != nil {
		return "", err
	}
	key := customPrefix + strconv.Itoa(n)
	if len(in.LogoData) > 0 {
		if err := os.MkdirAll(filepath.Dir(m.customLogoPath(key)), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(m.customLogoPath(key), in.LogoData, 0o644); err != nil {
			return "", err
		}
		logo = "upload:" + key
	}
	c := Custom{Key: key, Name: name, Logo: logo}
	if err := m.d.insertCustom(ctx, n, c, m.now()); err != nil {
		return "", err
	}
	var o Override
	if in.Category != "" {
		o.Category = &in.Category
	}
	if country != "" {
		o.Country = &country
	}
	if !o.empty() {
		if err := m.d.setOverride(ctx, key, o); err != nil {
			return "", err
		}
	}
	if group != "" {
		if err := m.d.setNameRule(ctx, group, &Rule{Channel: key}); err != nil {
			return "", err
		}
	}
	m.mu.Lock()
	m.pool.custom[key] = c
	if !o.empty() {
		m.pool.overrides[key] = o
	}
	if group != "" {
		m.pool.nameRules[group] = Rule{Channel: key}
	}
	m.mu.Unlock()
	m.rebuild(ctx)
	return key, nil
}

// customKeys — ключи своих каналов.
func (m *Module) customKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.pool.custom {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// customMatches — свои каналы, чьё название подходит под нормализованный запрос q: ранг 0 — совпало, 1 —
// начинается с, 2 — содержит; -1 — нет.
func (m *Module) customMatches(q string) []EPGChannel {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []EPGChannel
	for _, c := range m.pool.custom {
		if n := m3u.Norm(c.Name); strings.Contains(n, q) {
			out = append(out, EPGChannel{Key: c.Key, Name: c.Name})
		}
	}
	slices.SortFunc(out, func(a, b EPGChannel) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (d db) nextCustom(ctx context.Context) (int, error) {
	var n int
	err := d.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(n), 0) + 1 FROM iptv_custom`).Scan(&n)
	return n, err
}

func (d db) insertCustom(ctx context.Context, n int, c Custom, now time.Time) error {
	_, err := d.W.ExecContext(ctx, `INSERT INTO iptv_custom (key, n, name, logo, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.Key, n, c.Name, c.Logo, now.UnixMilli())
	return err
}

// serveUploadedLogo — загруженный логотип своего канала; false — у канала не загруженный.
func (m *Module) serveUploadedLogo(w http.ResponseWriter, r *http.Request, src string) bool {
	key, ok := strings.CutPrefix(src, "upload:")
	if !ok {
		return false
	}
	b, err := os.ReadFile(m.customLogoPath(key))
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(b)
	return true
}
