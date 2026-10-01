package app

import (
	"context"
	"strings"
	"time"

	"kinodom/internal/source/rutracker"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
)

// trackerStatus — трекер на экране «Состояние» и во вкладке каталога.
type trackerStatus struct {
	State     string               `json:"state"` // ok, warn, down, off (адрес не введён)
	Text      string               `json:"text,omitempty"`
	UpdatedAt *time.Time           `json:"updatedAt"` // последнее удачное обновление разделов; null — не было
	Login     *rutracker.LoginInfo `json:"login,omitempty"`
}

// kinopoiskStatus — Кинопоиск без токена и запасной ключ с квотой: последние известные модулю рейтингов.
type kinopoiskStatus struct {
	KeySet     bool             `json:"keySet"`
	BadKey     bool             `json:"badKey"`
	DailyUsed  int              `json:"dailyUsed"`
	DailyLimit int              `json:"dailyLimit"`
	TotalLimit int              `json:"totalLimit"` // −1 — общего лимита нет
	QuotaUntil *time.Time       `json:"quotaUntil"` // квота ключа кончилась — до; null — нет
	Keyless    kinopoiskKeyless `json:"keyless"`
}

// kinopoiskKeyless — Кинопоиск без токена (спека 11b, 5.7): пауза после отказа сайта и запросов за сутки.
type kinopoiskKeyless struct {
	PausedUntil *time.Time `json:"pausedUntil"` // null — работает
	Reason      string     `json:"reason"`
	Today       int        `json:"today"`
}

// timeOrNil — нулевое время — null в JSON.
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// streamsStatus — потоки всех модулей и скорости торрентов.
type streamsStatus struct {
	Count         int   `json:"count"`
	DownloadSpeed int64 `json:"downloadSpeed"` // байт/с
	UploadSpeed   int64 `json:"uploadSpeed"`
}

// trackerOf — состояние трекера по его проблемам: «не отвечает» хуже «частично» (раздел, форум,
// разбор, вход Rutracker).
func trackerOf(ps []store.Problem, tracker string) trackerStatus {
	st := trackerStatus{State: "ok"}
	for _, p := range ps {
		if p.ID == "catalog."+tracker+".address" { // адрес не введён — трекер выключен (этап 11a)
			return trackerStatus{State: "off", Text: p.Text}
		}
	}
	for _, p := range ps {
		switch {
		case p.ID == "catalog."+tracker:
			st.State, st.Text = "down", p.Text
		case st.State == "ok" && (strings.HasPrefix(p.ID, "catalog."+tracker+".") || strings.HasPrefix(p.ID, "catalog."+tracker+":") ||
			(tracker == "rutracker" && p.ID == "rutracker.login")):
			st.State, st.Text = "warn", p.Text
		}
	}
	return st
}

// statusFields — поля «Состояния» от модулей (спека этапа 7, раздел 5.7).
func (a *App) statusFields(ctx context.Context) (map[string]any, error) {
	ps, err := a.DB.Problems(ctx)
	if err != nil {
		return nil, err
	}
	trackers := map[string]trackerStatus{}
	for _, tr := range []string{"rutor", "rutracker"} {
		st := trackerOf(ps, tr)
		at, err := a.Catalog.UpdatedAt(ctx, tr)
		if err != nil {
			return nil, err
		}
		if !at.IsZero() {
			st.UpdatedAt = &at
		}
		if tr == "rutracker" {
			li := a.rutracker.LoginState()
			st.Login = &li
		}
		trackers[tr] = st
	}
	rs, err := a.Ratings.Status(ctx)
	if err != nil {
		return nil, err
	}
	var disk torrents.DiskInfo
	if a.Torrents.Engine() != nil {
		if disk, err = a.Torrents.Disk(ctx); err != nil {
			return nil, err
		}
	}
	down, up := a.Torrents.Speeds()
	updates, err := a.Follow.Unread(ctx) // колокольчик: строк в «Новых сериях» (спека 11b, 6.1)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"updates":   updates,
		"version":   a.version,
		"setupDone": a.Settings.Current().SetupDone, // пульт открывает мастер начальных настроек
		"trackers":  trackers,
		"kinopoisk": kinopoiskStatus{KeySet: rs.HasKey, BadKey: rs.BadKey, DailyUsed: rs.Quota.DailyUsed,
			DailyLimit: rs.Quota.DailyLimit, TotalLimit: rs.Quota.TotalLimit, QuotaUntil: timeOrNil(rs.PausedUntil),
			Keyless: kinopoiskKeyless{PausedUntil: timeOrNil(rs.Keyless.PausedUntil), Reason: rs.Keyless.Reason, Today: rs.Keyless.Today}},
		"disk":    disk,
		"streams": streamsStatus{Count: a.Power.Active(), DownloadSpeed: down, UploadSpeed: up},
		"iptv":    a.IPTV.Status(),
	}, nil
}
