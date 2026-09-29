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
	State     string               `json:"state"` // ok, warn, down
	Text      string               `json:"text,omitempty"`
	UpdatedAt *time.Time           `json:"updatedAt"` // последнее удачное обновление разделов; null — не было
	Login     *rutracker.LoginInfo `json:"login,omitempty"`
}

// kinopoiskStatus — ключ и квота Кинопоиска: последние известные модулю рейтингов.
type kinopoiskStatus struct {
	KeySet     bool `json:"keySet"`
	BadKey     bool `json:"badKey"`
	DailyUsed  int  `json:"dailyUsed"`
	DailyLimit int  `json:"dailyLimit"`
	TotalLimit int  `json:"totalLimit"` // −1 — общего лимита нет
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
	return map[string]any{
		"trackers": trackers,
		"kinopoisk": kinopoiskStatus{KeySet: rs.HasKey, BadKey: rs.BadKey, DailyUsed: rs.Quota.DailyUsed,
			DailyLimit: rs.Quota.DailyLimit, TotalLimit: rs.Quota.TotalLimit},
		"disk":    disk,
		"streams": streamsStatus{Count: a.Power.Active(), DownloadSpeed: down, UploadSpeed: up},
	}, nil
}
