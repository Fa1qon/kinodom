package app

import (
	"context"
	"database/sql"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/follow"
	"kinodom/internal/history"
	"kinodom/internal/library"
)

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
