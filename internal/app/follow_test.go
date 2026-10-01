package app

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

// Раздача сериала в API — признак сериала и подписка; «Следить» из домашней сети — active (спека 11b,
// 6.1, 6.6).
func TestReleaseSeriesAndFollow(t *testing.T) {
	a := rutorApp(t)
	id := rutorRelease(t, a)
	base := "http://" + a.API.Addr()
	url := base + "/api/v1/releases/" + strconv.FormatInt(id, 10)
	var v struct {
		Series bool   `json:"series"`
		Follow string `json:"follow"`
	}
	getJSON(t, url, &v)
	if !v.Series || v.Follow != "" {
		t.Fatalf("до «Следить»: %+v", v)
	}
	if code, body := putJSON(t, url+"/follow", nil); code != http.StatusNoContent {
		t.Fatalf("«Следить»: %d %s", code, body)
	}
	getJSON(t, url, &v)
	if v.Follow != "active" {
		t.Fatalf("после «Следить»: %+v", v)
	}
}

// Колокольчик: в «Состоянии» — сколько строк в «Новых сериях».
func TestStatusUpdatesCount(t *testing.T) {
	a := rutorApp(t)
	id := rutorRelease(t, a)
	base := "http://" + a.API.Addr()
	var st map[string]any
	getJSON(t, base+"/api/v1/status", &st)
	if st["updates"] != float64(0) {
		t.Fatalf("без оповещений: %v", st["updates"])
	}
	if _, err := a.DB.W.Exec(`INSERT INTO updates(release_id, kind, label, at) VALUES(?, 'removed', 'Раздача снята с трекера', 1)`, id); err != nil {
		t.Fatal(err)
	}
	getJSON(t, base+"/api/v1/status", &st)
	if st["updates"] != float64(1) {
		t.Fatalf("одно оповещение: %v", st["updates"])
	}
}

// «Загрузки»: у раздачи — признак сериала и подписка (кнопка «Следить» у сериала в «Загрузках»).
func TestDownloadsFollow(t *testing.T) {
	a := rutorApp(t)
	id := rutorRelease(t, a)
	base := "http://" + a.API.Addr()
	url := base + "/api/v1/releases/" + strconv.FormatInt(id, 10)
	postJSON(t, url+"/download", map[string]any{}, nil)
	if err := a.Follow.Follow(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Items []struct {
			Release *struct {
				ID     int64  `json:"id"`
				Series bool   `json:"series"`
				Follow string `json:"follow"`
			} `json:"release"`
		} `json:"items"`
	}
	waitUntil(t, "раздача в «Загрузках»", func() bool {
		getJSON(t, base+"/api/v1/downloads", &v)
		return len(v.Items) > 0
	})
	r := v.Items[0].Release
	if r == nil || r.ID != id || !r.Series || r.Follow != "active" {
		t.Fatalf("раздача в «Загрузках»: %+v", r)
	}
}
