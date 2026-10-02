package catalog

import (
	"context"
	"path"
	"slices"
	"strings"

	"kinodom/internal/meta"
)

// formatOf — формат раздачи (спека этапа 7, раздел 10.2): по файлам .torrent, иначе по описанию.
func (c *Catalog) formatOf(description string, torrent []byte) string {
	if len(torrent) > 0 && c.torrentFormat != nil {
		if f := c.torrentFormat(torrent); f != "" {
			return f
		}
	}
	return meta.FormatInText(description)
}

// fillFormats — формат раздачам, чьи страницы загружены до миграции 0008: один проход после старта,
// по 200 за запрос. Не найденный формат записывается как «неизвестен» — второй раз не ищется.
func (c *Catalog) fillFormats(ctx context.Context) error {
	for {
		rs, err := c.st.formatless(ctx, 200)
		if err != nil || len(rs) == 0 {
			return err
		}
		for _, r := range rs {
			if err := c.st.saveFormat(ctx, r.id, c.formatOf(r.description, r.torrent)); err != nil {
				return err
			}
		}
	}
}

// SetPreferredFormat — формат в приоритете из настроек; "" — нет (спека этапа 7, раздел 10.3).
func (c *Catalog) SetPreferredFormat(f string) {
	c.mu.Lock()
	c.preferred = f
	c.mu.Unlock()
}

// PreferredFormat — действующий формат в приоритете.
func (c *Catalog) PreferredFormat() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preferred
}

// FilePreferred — формат файла (по расширению) — формат в приоритете: «Загрузки» подсвечивают его (план 14А).
func FilePreferred(name, pref string) bool {
	ext := strings.TrimPrefix(path.Ext(strings.ReplaceAll(name, `\`, "/")), ".")
	return pref != "" && ext != "" && strings.EqualFold(ext, pref)
}

// FormatPreferred — основной формат format (первый в списке) — формат в приоритете pref.
func FormatPreferred(format, pref string) bool { return prefers(format, pref) }

// prefers — основной (самый большой) формат раздачи совпадает с форматом в приоритете.
func prefers(format, pref string) bool {
	main, _, _ := strings.Cut(format, ", ")
	return pref != "" && main == pref
}

// preferFirst — раздачи в формате в приоритете вперёд; внутри каждой части порядок прежний.
func preferFirst(rs []row, pref string) {
	if pref == "" {
		return
	}
	slices.SortStableFunc(rs, func(a, b row) int {
		pa, pb := prefers(a.Format, pref), prefers(b.Format, pref)
		switch {
		case pa && !pb:
			return -1
		case pb && !pa:
			return 1
		}
		return 0
	})
}
