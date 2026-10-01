package follow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"kinodom/internal/store"
)

var ctx = context.Background()

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// addRelease — раздача в каталоге (подписка ссылается на неё).
func addRelease(t *testing.T, db *store.DB, topic, title, hash string) int64 {
	t.Helper()
	res, err := db.W.Exec(`INSERT INTO releases(tracker, topic_id, title, infohash) VALUES('rutor', ?, ?, ?)`, topic, title, hash)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// Подписка: повторная — без ошибки и снова активна; отписка; раздачу удалили — подписка и оповещения
// уходят каскадом (спека 11b, 6.6).
func TestFollowStore(t *testing.T) {
	db := openDB(t)
	st := followDB{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	id := addRelease(t, db, "1", "Холод [01-07 из 08]", "aaa")
	if err := st.follow(ctx, Follow{Release: id, InfoHash: "aaa", Episodes: 7, Total: 8}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.setState(ctx, id, StateFinished); err != nil {
		t.Fatal(err)
	}
	if err := st.follow(ctx, Follow{Release: id, InfoHash: "bbb", Episodes: 8, Total: 8}, now); err != nil {
		t.Fatal(err)
	}
	f, ok, err := st.get(ctx, id)
	if err != nil || !ok || f.State != StateActive || f.InfoHash != "bbb" || f.Episodes != 8 {
		t.Fatalf("повторная подписка: %+v %v %v", f, ok, err)
	}
	if fs, _ := st.active(ctx); len(fs) != 1 || fs[0].Release != id {
		t.Fatalf("активные: %+v", fs)
	}
	if err := st.unfollow(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.get(ctx, id); ok {
		t.Fatal("отписка не удалила подписку")
	}
	st.follow(ctx, Follow{Release: id, InfoHash: "bbb"}, now)
	st.addUpdate(ctx, Update{Release: id, Kind: KindEpisodes, InfoHash: "bbb", Label: "1×08", Files: []UpdateFile{{7, "e08.mkv"}}, At: now})
	if _, err := db.W.Exec(`DELETE FROM releases WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.get(ctx, id); ok {
		t.Fatal("раздачу удалили — подписка осталась")
	}
	if us, _ := st.updates(ctx); len(us) != 0 {
		t.Fatalf("раздачу удалили — оповещения остались: %+v", us)
	}
}

// Оповещения: новые сверху, «Убрать» — пропадает.
func TestUpdatesDismiss(t *testing.T) {
	db := openDB(t)
	st := followDB{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	id := addRelease(t, db, "1", "Холод", "aaa")
	a, err := st.addUpdate(ctx, Update{Release: id, Kind: KindEpisodes, InfoHash: "aaa", Label: "1×07", Files: []UpdateFile{{6, "e07.mkv"}}, At: now})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := st.addUpdate(ctx, Update{Release: id, Kind: KindEpisodes, InfoHash: "aaa", Label: "1×08", Files: []UpdateFile{{7, "e08.mkv"}}, At: now.Add(time.Hour)})
	us, err := st.updates(ctx)
	if err != nil || len(us) != 2 || us[0].ID != b || us[1].Files[0].Path != "e07.mkv" || !us[0].At.Equal(now.Add(time.Hour)) {
		t.Fatalf("оповещения: %+v %v", us, err)
	}
	if err := st.dismiss(ctx, a); err != nil {
		t.Fatal(err)
	}
	if us, _ := st.updates(ctx); len(us) != 1 || us[0].ID != b {
		t.Fatalf("после «Убрать»: %+v", us)
	}
}

// Переход раздачи на новую версию: infohash оповещений — новый, номера файлов — по сопоставлению, файлы без
// пары — убираются из оповещения; известную версию подписки модуль сдвигает сам, записав оповещение (иначе
// сбой после перехода терял серию — финальное ревью 11b-В).
func TestFollowRekeyTx(t *testing.T) {
	db := openDB(t)
	st := followDB{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	id := addRelease(t, db, "1", "Холод", "old")
	st.follow(ctx, Follow{Release: id, InfoHash: "old", Episodes: 7}, now)
	st.addUpdate(ctx, Update{Release: id, Kind: KindEpisodes, InfoHash: "old", Label: "1×06–1×07",
		Files: []UpdateFile{{5, "e06.mkv"}, {6, "e07.mkv"}}, At: now})
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RekeyTx(ctx, tx, "old", "new", map[int]int{6: 7}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f, _, _ := st.get(ctx, id)
	us, _ := st.updates(ctx)
	if f.InfoHash != "old" || len(us) != 1 || us[0].InfoHash != "new" || len(us[0].Files) != 1 || us[0].Files[0] != (UpdateFile{7, "e07.mkv"}) {
		t.Fatalf("после перехода: %+v %+v", f, us)
	}
}
