package library

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"kinodom/internal/meta"
)

var (
	ErrNoUnit  = errors.New("такой единицы в медиатеке нет")
	ErrBadKP   = errors.New("ссылка на Кинопоиск — вида kinopoisk.ru/film/301 или номер фильма")
	ErrNoTitle = errors.New("нужно название")
)

var reKPLink = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?kinopoisk\.ru/(?:film|series)/(\d+)`)

// parseKP — номер Кинопоиска из ссылки (kinopoisk.ru/film/301, …/series/…) или из числа.
func parseKP(s string) (int, error) {
	s = strings.TrimSpace(s)
	if m := reKPLink.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, ErrBadKP
	}
	return n, nil
}

// setUnit — правка единицы; ErrNoUnit — нет такой.
func (d db) setUnit(ctx context.Context, unit int64, query string, args ...any) error {
	res, err := d.W.ExecContext(ctx, query, append(args, unit)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoUnit
	}
	return nil
}

// LinkKP — «Это другой фильм» / «Привязать»: номер Кинопоиска из ссылки; описание и постер
// загружаются сразу.
func (l *Library) LinkKP(ctx context.Context, unit int64, link string) error {
	kp, err := parseKP(link)
	if err != nil {
		return err
	}
	if err := l.d.setUnit(ctx, unit, `UPDATE lib_units SET kp_id = ?, state = 'linked', manual_title = '', manual_year = 0 WHERE id = ?`, kp); err != nil {
		return err
	}
	return l.refresh(ctx, unit, false)
}

// MarkManual — «Разметить вручную»: название и год без Кинопоиска.
func (l *Library) MarkManual(ctx context.Context, unit int64, title string, year int) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return ErrNoTitle
	}
	if err := l.d.setUnit(ctx, unit, `UPDATE lib_units SET kp_id = 0, state = 'manual', manual_title = ?, manual_year = ? WHERE id = ?`,
		title, max(year, 0)); err != nil {
		return err
	}
	return l.refresh(ctx, unit, false)
}

// SearchAgain — «Искать снова»: единица снова ищется на Кинопоиске (пауза по квоте — снимается).
func (l *Library) SearchAgain(ctx context.Context, unit int64) error {
	if err := l.d.setUnit(ctx, unit, `UPDATE lib_units SET state = 'new', attempts = 0, search_at = 0 WHERE state IN ('unrecognized', 'wait', 'new') AND id = ?`); err != nil {
		var n int
		if l.d.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM lib_units WHERE id = ?`, unit).Scan(&n); n == 0 {
			return ErrNoUnit
		}
	}
	return l.refresh(ctx, unit, true)
}

// ResetUnit — вернуть автоматику: снять ссылку и ручную разметку, искать заново.
func (l *Library) ResetUnit(ctx context.Context, unit int64) error {
	if err := l.d.setUnit(ctx, unit, `UPDATE lib_units SET kp_id = 0, manual_title = '', manual_year = 0, state = 'new', attempts = 0, search_at = 0 WHERE id = ?`); err != nil {
		return err
	}
	return l.refresh(ctx, unit, true)
}

// refresh — после правки единицы из пульта: распознавание и данные карточки только этой единицы (хвост
// Х10: вся очередь синхронно держала «Привязать» десятки секунд и тратила квоту дважды); остальную
// очередь разберёт обход — он будится. again — снять паузу квоты.
func (l *Library) refresh(ctx context.Context, unit int64, again bool) error {
	ctx = meta.Urgent(ctx) // ждёт человек — очередь каталога, а не медиатеки
	if again {
		l.mu.Lock()
		l.kpPause = l.now()
		l.mu.Unlock()
	}
	tus, err := l.torrents(ctx)
	if err != nil {
		tus = nil
	}
	if err := l.recognizePending(ctx, tus, unit); err != nil {
		return err
	}
	var kp int
	if err := l.d.R.QueryRowContext(ctx, `SELECT kp_id FROM lib_units WHERE id = ?`, unit).Scan(&kp); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := l.refreshCards(ctx, tus, max(kp, 0), kp == 0); err != nil {
		return err
	}
	l.wakeRecognizer()
	return nil
}

// UnrecognizedView — единица на вкладке «Не распознано».
type UnrecognizedView struct {
	Unit     int64  `json:"unit"`
	Name     string `json:"name"`  // имя папки или файла, раздачи
	Title    string `json:"title"` // разобранное название
	Year     int    `json:"year"`
	Path     string `json:"path"` // путь папки или файла; у скачанного — папка раздачи
	Category string `json:"category"`
	Card     string `json:"card"` // ключ карточки
}

// Unrecognized — нераспознанные единицы, видимые на устройстве.
func (l *Library) Unrecognized(ctx context.Context, device string) ([]UnrecognizedView, error) {
	v, err := l.view(ctx, device)
	if err != nil {
		return nil, err
	}
	out := []UnrecognizedView{}
	for key, us := range v.units {
		if !v.shown(key) {
			continue
		}
		for _, u := range us {
			if u.State != StateUnrecognized {
				continue
			}
			x := UnrecognizedView{Unit: u.ID, Name: u.Name, Title: u.Title, Year: u.Year, Path: u.Key,
				Category: v.byID[v.category(key)].Name, Card: key}
			if u.tu != nil {
				x.Path = u.tu.Dir
			}
			out = append(out, x)
		}
	}
	sortUnrecognized(out)
	return out, nil
}

func sortUnrecognized(out []UnrecognizedView) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && natCompare(out[j].Name, out[j-1].Name) < 0; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}

// folderPaths — папки категории сейчас (для проверки только новых при правке).
func (d db) folderPaths(ctx context.Context, category int64) (map[string]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT path_key FROM lib_folders WHERE category = ?`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// checkNewFolders — новые папки категории: локальный диск, есть, читается, не папка загрузок.
func (l *Library) checkNewFolders(ctx context.Context, category int64, paths []string) error {
	have := map[string]bool{}
	if category > 0 {
		var err error
		if have, err = l.d.folderPaths(ctx, category); err != nil {
			return err
		}
	}
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" || have[pathKey(p)] {
			continue
		}
		if err := checkFolder(p, l.o.DownloadsDir()); err != nil {
			return err
		}
	}
	return nil
}

// AddCategory, UpdateCategory, DeleteCategory, SetDeviceCategory — правки категорий из пульта; после
// них медиатека обходится заново.
func (l *Library) AddCategory(ctx context.Context, in CategoryInput) (int64, error) {
	if err := l.checkNewFolders(ctx, 0, in.Folders); err != nil {
		return 0, err
	}
	id, err := l.d.addCategory(ctx, in)
	if err == nil {
		l.startScan(true)
	}
	return id, err
}

func (l *Library) UpdateCategory(ctx context.Context, id int64, in CategoryInput) error {
	if err := l.checkNewFolders(ctx, id, in.Folders); err != nil {
		return err
	}
	err := l.d.updateCategory(ctx, id, in)
	if err == nil {
		l.startScan(true)
	}
	return err
}

func (l *Library) DeleteCategory(ctx context.Context, id int64) error {
	return l.d.deleteCategory(ctx, id)
}

func (l *Library) SetDeviceCategory(ctx context.Context, device string, id int64, on bool) error {
	return l.d.setDeviceCategory(ctx, device, id, on)
}
