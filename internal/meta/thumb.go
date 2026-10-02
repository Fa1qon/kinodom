package meta

import (
	"image"
	_ "image/gif" // постеры GIF — первый кадр
	"image/jpeg"
	_ "image/png"
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
)

// thumb — путь к миниатюре оригинала orig (ключ key): готовая — она, иначе делается сейчас. false — отдавать
// оригинал: он не шире thumbWidth или не разбирается (битый, неизвестный формат); и то и другое запоминается —
// оригинал не разбирается на каждый запрос, о битом — строка в журнал один раз.
func (im *Images) thumb(key, orig string) (string, bool) {
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
	im.thumbSem <- struct{}{}
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
// запись не оставит битую миниатюру). false без ошибки — оригинал и так не шире.
func makeThumb(orig, dst string) (bool, error) {
	f, err := os.Open(orig)
	if err != nil {
		return false, err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return false, err
	}
	b := src.Bounds()
	if b.Dx() <= thumbWidth {
		return false, nil
	}
	h := max(1, b.Dy()*thumbWidth/b.Dx())
	m := image.NewRGBA(image.Rect(0, 0, thumbWidth, h))
	xdraw.CatmullRom.Scale(m, m.Bounds(), src, b, xdraw.Src, nil)
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
