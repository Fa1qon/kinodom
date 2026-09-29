package meta

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"kinodom/internal/netx"
)

// ErrNoImage — по адресу не картинка: заглушка хостинга, «нет постера» Кинопоиска, не http(s).
var ErrNoImage = errors.New("картинки нет")

// Via — как качать картинку: картинки со страниц раздач — через прокси трекеров, постеры
// Кинопоиска — напрямую (спека, разделы 2 и 5).
type Via int

const (
	ViaProxy Via = iota
	Direct
)

type ImagesOptions struct {
	Dir      string        // папка кэша (config.Paths.Images)
	Proxy    *netx.Proxy   // прокси для трекеров; nil — как у системы
	Rate     rate.Limit    // 0 — 2 запроса/с на хостинги картинок (спека, раздел 5)
	Timeout  time.Duration // 0 — 60 с
	MaxBytes int64         // 0 — 10 МБ
	Log      *slog.Logger  // nil — без журнала
	// AllowPrivate — тесты: картинки с адресов этого ПК (фейковые хостинги). В работе адреса этого
	// ПК и домашней сети не скачиваются: они приходят из описаний раздач (ревью 5b, M10).
	AllowPrivate bool
}

// Images — картинки, которые сервер скачивает к себе и отдаёт клиентам сам (спека, раздел 8):
// телевизоры не ходят на хостинги картинок и в Кинопоиск. Ключ картинки — хэш её адреса,
// файл — <ключ>.<расширение> в папке кэша.
type Images struct {
	o       ImagesOptions
	proxied *http.Client
	direct  *http.Client
	lim     *rate.Limiter

	mu      sync.Mutex
	noImage map[string]time.Time // адрес → когда оказалось, что там не картинка
}

// noImageFor — адрес, где не картинка (заглушка хостинга, страница), не качается снова столько:
// каталог спрашивает постеры сотен раздач, и та же заглушка не должна скачиваться каждый раз
// (ревью 5b, M4).
const noImageFor = 24 * time.Hour

var imageExt = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}

var reImageKey = regexp.MustCompile(`^[0-9a-f]{40}$`)

// browserUA — хостинги картинок отвечают 404 на User-Agent Go по умолчанию (fastpic, проверено
// вживую 2026-09-28): картинки качаются как браузером.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

func NewImages(o ImagesOptions) (*Images, error) {
	if o.Rate == 0 {
		o.Rate = 2
	}
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 10 << 20
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("папка картинок: %w", err)
	}
	ptr, dtr := netx.NewTransport(o.Proxy), netx.NewTransport(nil)
	if !o.AllowPrivate {
		netx.PublicOnly(ptr, o.Proxy)
		netx.PublicOnly(dtr, nil)
	}
	return &Images{o: o, proxied: &http.Client{Transport: ptr, Timeout: o.Timeout},
		direct: &http.Client{Transport: dtr, Timeout: o.Timeout}, lim: rate.NewLimiter(o.Rate, 1),
		noImage: map[string]time.Time{}}, nil
}

// ImageKey — ключ картинки для адреса: /img/{ключ}.
func ImageKey(src string) string {
	sum := sha1.Sum([]byte(src))
	return hex.EncodeToString(sum[:])
}

// Fetch скачивает картинку к себе (если её ещё нет) и возвращает ключ для /img/{ключ}.
// Не картинка — ErrNoImage, файл не создаётся.
func (im *Images) Fetch(ctx context.Context, src string, via Via) (string, error) {
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(!im.o.AllowPrivate && netx.PrivateHost(u.Hostname())) {
		return "", ErrNoImage
	}
	key := ImageKey(src)
	if im.find(key) != "" {
		return key, nil
	}
	im.mu.Lock()
	at, known := im.noImage[src]
	im.mu.Unlock()
	if known && time.Since(at) < noImageFor {
		return "", ErrNoImage
	}
	key, err = im.fetch(ctx, u, src, key, via)
	if errors.Is(err, ErrNoImage) {
		im.mu.Lock()
		im.noImage[src] = time.Now()
		im.mu.Unlock()
	}
	return key, err
}

// fetch — сама загрузка картинки в кэш.
func (im *Images) fetch(ctx context.Context, u *url.URL, src, key string, via Via) (string, error) {
	if err := im.lim.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("картинки: очередь не успевает до срока: %w", context.DeadlineExceeded)
	}
	client := im.proxied
	if via == Direct {
		client = im.direct
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", ErrNoImage
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("картинка %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("картинка %s: ответ %d", u.Host, resp.StatusCode)
	}
	if strings.Contains(resp.Request.URL.Path, "no-poster") { // Кинопоиск: «постера нет»
		return "", ErrNoImage
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, im.o.MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("картинка %s: ответ оборвался: %w", u.Host, err)
	}
	if int64(len(body)) > im.o.MaxBytes {
		return "", fmt.Errorf("картинка %s: больше %d МБ", u.Host, im.o.MaxBytes>>20)
	}
	ext, ok := imageExt[http.DetectContentType(body)]
	if !ok {
		return "", ErrNoImage
	}
	// Сначала во временный файл: оборванная запись не должна выглядеть готовой картинкой.
	tmp, err := os.CreateTemp(im.o.Dir, key+"-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(im.o.Dir, key+ext)); err != nil {
		os.Remove(tmp.Name())
		// Ту же картинку одновременно скачал другой запрос: на Windows второе переименование в
		// занятый файл — «Access is denied», хотя картинка уже в кэше (ревью 5b, M4).
		if im.find(key) != "" {
			return key, nil
		}
		return "", err
	}
	return key, nil
}

// find — путь к файлу картинки или "".
func (im *Images) find(key string) string {
	for _, ext := range imageExt {
		p := filepath.Join(im.o.Dir, key+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Handler — GET /img/{key}: картинка из кэша. Ключ — только 40 шестнадцатеричных знаков: файлы
// вне папки кэша не отдаются. Картинка по ключу не меняется — кэшировать можно надолго.
func (im *Images) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if !reImageKey.MatchString(key) {
			http.NotFound(w, r)
			return
		}
		p := im.find(key)
		if p == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, p)
	})
}

// Sweep чистит кэш (хвост этапа 5b: иначе ~2 ГБ на 10 000 постеров): удаляет картинки, которые
// не нужны (keep(ключ) == false) и лежат дольше суток — свежие мог только что скачать кто-то ещё, —
// и обрывки *.tmp старше часа (запись оборвалась при аварии). Возвращает ключи удалённых картинок.
func (im *Images) Sweep(keep func(key string) bool, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(im.o.Dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		name, age := e.Name(), now.Sub(info.ModTime())
		if strings.HasSuffix(name, ".tmp") {
			if age > time.Hour {
				os.Remove(filepath.Join(im.o.Dir, name))
			}
			continue
		}
		key := strings.TrimSuffix(name, filepath.Ext(name))
		if !reImageKey.MatchString(key) || age < 24*time.Hour || keep(key) {
			continue
		}
		if err := os.Remove(filepath.Join(im.o.Dir, name)); err == nil {
			removed = append(removed, key)
		}
	}
	return removed, nil
}
