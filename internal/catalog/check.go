package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/source"
)

// Version — раздача на трекере сейчас (подписка на новые серии, спека 11b, 6.2).
type Version struct {
	InfoHash string
	Title    string
	Magnet   string // с трекерами источника
	Torrent  []byte // .torrent этой версии (Rutor); nil — только magnet (Rutracker)
}

// CheckRelease — страница раздачи заново, через клиент трекера (1 запрос/с, прокси, зеркала): название,
// infohash, magnet; у Rutor — .torrent этой версии (сохранённый, если он той же версии, иначе новый).
// Новая версия записывается в каталог: «Скачать» и «Другие раздачи» видят уже её. Снята с трекера —
// source.ErrRemoved (и раздача помечена снятой); трекер выключен, на паузе или не ответил — ошибка,
// повтор при следующей проверке.
func (c *Catalog) CheckRelease(ctx context.Context, id int64) (Version, error) {
	r, _, _, torrent, _, err := c.st.release(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNoRelease
	}
	if err != nil {
		return Version{}, err
	}
	src, ok := c.sources[r.Tracker]
	if !ok || !c.configured(r.Tracker) {
		return Version{}, fmt.Errorf("%s выключен — адрес не задан", title(r.Tracker))
	}
	if until := c.forumPausedUntil(r.Tracker); until.After(c.now()) {
		return Version{}, fmt.Errorf("%s: страницы раздач на паузе до %s", title(r.Tracker), until.Format("15:04"))
	}
	d, err := src.Details(ctx, r.TopicID)
	if errors.Is(err, source.ErrRemoved) {
		if err := c.st.markRemoved(ctx, id); err != nil {
			return Version{}, err
		}
		return Version{}, source.ErrRemoved
	}
	if err != nil {
		return Version{}, err
	}
	v := Version{InfoHash: d.InfoHash, Title: d.Title, Magnet: d.Magnet}
	if v.InfoHash == "" {
		// Хэша на странице нет (Rutracker сменил разметку, план 14А) — хэш из списка раздела: он свежий.
		v.InfoHash = r.InfoHash
	}
	if v.InfoHash == "" {
		return Version{}, fmt.Errorf("%s: на странице раздачи %s нет infohash", title(r.Tracker), r.TopicID)
	}
	if mb, ok := src.(magnetBuilder); ok {
		v.Magnet = mb.Magnet(v.InfoHash) // со своими трекерами: метаинфо от пиров за секунды (11b-В, задача 1)
	}
	if v.Title == "" {
		v.Title = r.Title
	}
	if sameVersion(torrent, v.InfoHash) {
		v.Torrent = torrent
	} else if tf, ok := src.(torrentFetcher); ok {
		tctx, cancel := context.WithTimeout(ctx, torrentWait)
		b, err := tf.Torrent(tctx, r.TopicID)
		cancel()
		if err != nil {
			return Version{}, err
		}
		v.Torrent = b
	}
	d.Magnet = v.Magnet
	if err := c.st.saveVersion(ctx, id, d, v.Torrent, c.now()); err != nil {
		return Version{}, err
	}
	return v, nil
}

// sameVersion — сохранённый .torrent — той же версии раздачи (infohash совпал).
func sameVersion(torrent []byte, infohash string) bool {
	if len(torrent) == 0 {
		return false
	}
	mi, err := metainfo.Load(bytes.NewReader(torrent))
	return err == nil && mi.HashInfoBytes().HexString() == infohash
}
