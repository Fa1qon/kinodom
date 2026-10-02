package meta

import (
	"context"
	"image"
	"image/color"
	_ "image/gif" // постеры GIF — первый кадр
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // большая часть постеров — WebP
)

// Миниатюры постеров (план 16А): сетки пульта на телевизоре разжимали постеры до 425 КБ секундами (замер
// 2026-10-02) — им отдаётся копия шириной thumbWidth, сделанная один раз и лежащая в кэше рядом с оригиналом.
const (
	thumbWidth   = 400         // сетка ~210 CSS px при плотности экрана ТВ 2
	thumbSuffix  = ".w400.jpg" // {key}.w400.jpg
	thumbQuality = 82
	thumbAtOnce  = 2 // миниатюр делается одновременно: сетка просит до 40 сразу
	// thumbMaxPixels — больше не разбирается (ревью 16А): картинка со страницы раздачи на 10 МБ может быть
	// 12000 × 12000 — сотни МБ памяти при разборе; такой «постер» отдаётся как есть.
	thumbMaxPixels = 40_000_000
)

// thumbBack — фон прозрачного в миниатюре: фон карточки постера в пульте (#1A1D21), а не чёрный JPEG (ревью 16А).
var thumbBack = color.RGBA{0x1A, 0x1D, 0x21, 0xFF}

// thumb — путь к миниатюре оригинала orig (ключ key): готовая — она, иначе делается сейчас. false — отдавать
// оригинал: он не шире thumbWidth, слишком велик или не разбирается (битый, неизвестный формат); это запоминается —
// оригинал не разбирается на каждый запрос, о битом — строка в журнал один раз. Очередь занята, а просьбу уже
// отменили (листали дальше), — оригинал, не ждать (ревью 16А).
func (im *Images) thumb(ctx context.Context, key, orig string) (string, bool) {
	p := filepath.Join(im.o.Dir, key+thumbSuffix)
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	im.mu.Lock()
	_, skip := im.thumbSkip[key]
	im.mu.Unlock()
	if skip {
		return "", false
	}
	select {
	case im.thumbSem <- struct{}{}:
	case <-ctx.Done():
		return "", false
	}
	defer func() { <-im.thumbSem }()
	if _, err := os.Stat(p); err == nil { // сделал соседний запрос, пока этот ждал
		return p, true
	}
	made, err := makeThumb(orig, p)
	if err != nil || !made {
		im.mu.Lock()
		im.thumbSkip[key] = err != nil
		im.mu.Unlock()
		if err != nil {
			im.o.Log.Warn("картинки: миниатюра не сделана — отдаётся оригинал", "key", key, "err", err)
		}
		return "", false
	}
	return p, true
}

// thumbFailures — сколько оригиналов не разобралось (для тестов).
func (im *Images) thumbFailures() int {
	im.mu.Lock()
	defer im.mu.Unlock()
	n := 0
	for _, bad := range im.thumbSkip {
		if bad {
			n++
		}
	}
	return n
}

// makeThumb — уменьшенная копия orig шириной thumbWidth в JPEG по пути dst (через временный файл: оборванная
// запись не оставит битую миниатюру). false без ошибки — оригинал и так не шире или слишком велик (размер — по
// заголовку, без разбора).
func makeThumb(orig, dst string) (bool, error) {
	f, err := os.Open(orig)
	if err != nil {
		return false, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return false, err
	}
	if cfg.Width <= thumbWidth || cfg.Width*cfg.Height > thumbMaxPixels {
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return false, err
	}
	b := src.Bounds()
	h := max(1, b.Dy()*thumbWidth/b.Dx())
	m := image.NewRGBA(image.Rect(0, 0, thumbWidth, h))
	xdraw.Draw(m, m.Bounds(), image.NewUniform(thumbBack), image.Point{}, xdraw.Src)
	xdraw.CatmullRom.Scale(m, m.Bounds(), src, b, xdraw.Over, nil)
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+"-*.tmp")
	if err != nil {
		return false, err
	}
	err = jpeg.Encode(tmp, m, &jpeg.Options{Quality: thumbQuality})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return false, err
	}
	return true, nil
}
