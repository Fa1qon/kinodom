// Package source — общий вид источников раздач (трекеров): топ категории, поиск, страница
// раздачи. Реализации — в подпакетах rutor (этап 3) и rutracker (этап 4); каталог (этап 5)
// работает только через интерфейс Source.
package source

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"kinodom/internal/netx"
)

// Source — источник раздач (спека, раздел 6).
type Source interface {
	Name() string
	Categories(ctx context.Context) ([]Category, error)                       // разделы для настроек
	Top(ctx context.Context, categoryID string, limit int) ([]Release, error) // по раздающим, убыв.
	Search(ctx context.Context, query string) ([]Release, error)              // только видеокатегории; найденное может прийти вместе с *PartialError
	Details(ctx context.Context, topicID string) (Details, error)             // страница раздачи
}

// Category — раздел трекера.
type Category struct {
	ID       string
	Name     string
	ParentID string // "" — верхний уровень
}

// Release — строка списка раздач: всё, что нужно каталогу без догрузки страницы раздачи.
type Release struct {
	Tracker    string // имя источника: "rutor", "rutracker"
	TopicID    string // номер раздачи на трекере
	Title      string
	CategoryID string // "" — неизвестна
	Seeders    int
	Leechers   int
	Size       int64 // байты; у Rutor в списке — округлённые
	Added      time.Time
	InfoHash   string // 40 hex-символов в нижнем регистре; "" — неизвестен
}

// Details — страница раздачи. Поля Release — свежие, со страницы.
type Details struct {
	Release
	Description string // текст без разметки, строки разделены \n
	PosterURL   string // абсолютный адрес; "" — картинки нет
	KinopoiskID string // номер фильма из ссылки в описании; "" — ссылки нет
	IMDbID      string
	Magnet      string
	TorrentURL  string // адрес .torrent; "" — только magnet
}

// ErrRemoved — раздачу удалили с трекера (проверять errors.Is). Та же ошибка, что у netx.
var ErrRemoved = netx.ErrRemoved

// ErrNoSection — раздела (форума) на трекере нет или в нём нет раздач: у Rutracker API — 404 (форум
// подборок ссылок, вживую 11b-Г). Каталог такой форум подраздела пропускает (проверять errors.Is).
var ErrNoSection = errors.New("раздела нет на трекере")

// KinopoiskLink — ссылка на фильм или сериал Кинопоиска, в том числе старого вида
// kinopoisk.ru/level/1/film/{id}/; подгруппа 1 — номер. С ним каталогу не нужен поиск по
// названию — это экономит квоту (спека, раздел 8).
var KinopoiskLink = regexp.MustCompile(`kinopoisk\.ru/(?:level/\d+/)?(?:film|series)/(\d+)`)

// ParseError — страница пришла, но нужного блока на ней нет: трекер, скорее всего, изменил
// разметку. Зеркало при этом не меняется (спека, раздел 5).
type ParseError struct {
	Tracker string // «Rutor»
	Block   string // «таблица раздач»
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s: на странице не найден блок «%s» — похоже, трекер изменил разметку", e.Tracker, e.Block)
}

// PartialError — поиск прошёл не везде: часть запросов не ответила или вышло время. Search
// возвращает найденное вместе с этой ошибкой: показать можно, но считать поиск полным и класть
// в кэш как полный — нельзя. errors.Is и errors.As доходят до причины (в том числе до
// context.DeadlineExceeded).
type PartialError struct {
	Tracker       string // «Rutor»
	Failed, Total int    // сколько запросов не ответило из скольких
	Err           error  // причина: отмена или первая ошибка
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%s: поиск прошёл не везде — не ответили %d из %d категорий: %v", e.Tracker, e.Failed, e.Total, e.Err)
}

func (e *PartialError) Unwrap() error { return e.Err }
