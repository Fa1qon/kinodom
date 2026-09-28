-- Раздачи: метаинфо хранится, чтобы после перезапуска не ждать метаданные от пиров.
CREATE TABLE torrents (
    infohash TEXT PRIMARY KEY,           -- 40 символов hex, нижний регистр
    name     TEXT NOT NULL DEFAULT '',
    metainfo BLOB,                       -- bencode; NULL, пока метаданные не получены
    source   TEXT NOT NULL DEFAULT '',   -- magnet или «torrent-file»
    added_at INTEGER NOT NULL
);

-- Файлы, выбранные для просмотра: хранятся и докачиваются (очистка — этап 6).
CREATE TABLE stored_files (
    infohash       TEXT NOT NULL REFERENCES torrents(infohash) ON DELETE CASCADE,
    file_index     INTEGER NOT NULL,
    path           TEXT NOT NULL,
    size           INTEGER NOT NULL,
    completed      INTEGER NOT NULL DEFAULT 0,
    last_opened_at INTEGER NOT NULL DEFAULT 0,
    last_stream_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (infohash, file_index)
);
