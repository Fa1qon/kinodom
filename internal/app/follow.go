package app

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/follow"
	"kinodom/internal/history"
	"kinodom/internal/library"
)

// initFollow — подписка на новые серии (спека 11b, раздел 6). KINODOM_FOLLOW_EVERY — интервал проверки
// для тестового сервера (проверка вживую), по умолчанию 6 часов.
func (a *App) initFollow(ctx context.Context) {
	every, _ := time.ParseDuration(os.Getenv("KINODOM_FOLLOW_EVERY"))
	a.Follow = follow.New(follow.Options{DB: a.DB, Catalog: a.Catalog, Torrents: a.Torrents, History: a.History,
		Rekey: rekeyUpgrade, Types: a.kpType, Every: every, Log: a.Log.With("module", "follow")})
	a.Follow.Register(a.API)
	a.Sup.Add(a.Follow, a.ModuleEnabled(ctx, a.Follow.Name()))
}

// kpType — вид фильма Кинопоиска по номеру из базы рейтингов ("" — неизвестен).
func (a *App) kpType(ctx context.Context, kp int) (string, error) {
	fs, err := a.Ratings.Films(ctx, []int{kp})
	if err != nil {
		return "", err
	}
	return fs[kp].Type, nil
}

// rekeyUpgrade — переход скачанной раздачи на обновлённую версию (спека 11b, 6.3.4): ключи (infohash,
// номер файла) истории просмотров, медиатеки и подписок — внутри одной транзакции с реестром торрентов.
func rekeyUpgrade(ctx context.Context, tx *sql.Tx, old, new metainfo.Hash, index map[int]int) error {
	o, n := old.HexString(), new.HexString()
	if err := history.RekeyTx(ctx, tx, o, n, index); err != nil {
		return err
	}
	if err := library.RekeyTx(ctx, tx, o, n, index); err != nil {
		return err
	}
	return follow.RekeyTx(ctx, tx, o, n, index)
}
