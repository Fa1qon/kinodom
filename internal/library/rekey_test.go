package library

import (
	"context"
	"testing"
)

// Переход раздачи на обновлённую версию (спека 11b, 6.3.4; Review Focus 4): единица медиатеки та же
// (номер, ручная привязка Кинопоиска, категория карточки), ключ — новый infohash, номера файлов — по
// сопоставлению; файл без пары уходит; единица новой версии, заведённая обходом раньше, — убирается.
func TestRekeyKeepsLibraryUnit(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := d.W.Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	const old, new = "aaaa000000000000000000000000000000000001", "bbbb000000000000000000000000000000000002"
	unit := exec(`INSERT INTO lib_units(source, key, name, kp_id, state, manual_title, added_at) VALUES('torrent', ?, 'Холод', 7036356, 'linked', 'Холод', 1)`, old)
	exec(`INSERT INTO lib_files(unit, path, tindex) VALUES(?, 'e01.mkv', 0), (?, 'e02.mkv', 1), (?, 'e03.mkv', 2)`, unit, unit, unit)
	exec(`INSERT INTO lib_cards(key, category) VALUES(?, 5)`, "kp-7036356")
	dup := exec(`INSERT INTO lib_units(source, key, name, added_at) VALUES('torrent', ?, 'Холод', 2)`, new)
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RekeyTx(ctx, tx, old, new, map[int]int{0: 0, 1: 2}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var key, state string
	var kp int
	if err := d.R.QueryRow(`SELECT key, kp_id, state FROM lib_units WHERE id = ?`, unit).Scan(&key, &kp, &state); err != nil {
		t.Fatal(err)
	}
	if key != new || kp != 7036356 || state != "linked" {
		t.Fatalf("единица: %s %d %s", key, kp, state)
	}
	var n int
	d.R.QueryRow(`SELECT COUNT(*) FROM lib_units WHERE id = ?`, dup).Scan(&n)
	if n != 0 {
		t.Fatal("единица новой версии из обхода осталась — дубль")
	}
	rows, err := d.R.Query(`SELECT path, tindex FROM lib_files WHERE unit = ? ORDER BY tindex`, unit)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		var i int
		rows.Scan(&p, &i)
		got = append(got, p+":"+string(rune('0'+i)))
	}
	if len(got) != 2 || got[0] != "e01.mkv:0" || got[1] != "e02.mkv:2" {
		t.Fatalf("файлы: %v", got)
	}
	var cat int
	d.R.QueryRow(`SELECT category FROM lib_cards WHERE key = 'kp-7036356'`).Scan(&cat)
	if cat != 5 {
		t.Fatalf("категория карточки: %d", cat)
	}
}
