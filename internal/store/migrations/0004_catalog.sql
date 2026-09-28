-- Каталог: модуль catalog (спека, раздел 7). Время — миллисекунды Unix.

-- Раздачи трекеров: из топов, поиска и страниц раздач.
CREATE TABLE releases (
    id           INTEGER PRIMARY KEY,
    tracker      TEXT NOT NULL,                -- rutor, rutracker
    topic_id     TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',     -- '' — ещё не загружено (топ Rutracker без названий)
    category_id  TEXT NOT NULL DEFAULT '',
    seeders      INTEGER NOT NULL DEFAULT 0,
    leechers     INTEGER NOT NULL DEFAULT 0,
    size         INTEGER NOT NULL DEFAULT 0,
    added_at     INTEGER NOT NULL DEFAULT 0,
    infohash     TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    poster_url   TEXT NOT NULL DEFAULT '',     -- постер со страницы раздачи
    image_key    TEXT NOT NULL DEFAULT '',     -- скачанная картинка (/img/{key}); '' — нет
    kinopoisk_id INTEGER NOT NULL DEFAULT 0,
    imdb_id      TEXT NOT NULL DEFAULT '',
    magnet       TEXT NOT NULL DEFAULT '',
    torrent      BLOB,                         -- .torrent Rutor, скачанный заранее (спека, раздел 7)
    details_at   INTEGER NOT NULL DEFAULT 0,   -- когда загружена страница раздачи; 0 — ещё нет
    retry_at     INTEGER NOT NULL DEFAULT 0,   -- страница не загрузилась — не раньше
    removed      INTEGER NOT NULL DEFAULT 0,   -- раздачу удалили с трекера
    updated_at   INTEGER NOT NULL DEFAULT 0,   -- когда обновлены цифры
    UNIQUE (tracker, topic_id)
);
CREATE INDEX releases_infohash ON releases(infohash);

-- Топы разделов на момент последнего удачного обновления.
CREATE TABLE catalog_entries (
    tracker     TEXT NOT NULL,
    category_id TEXT NOT NULL,
    position    INTEGER NOT NULL,
    release_id  INTEGER NOT NULL REFERENCES releases(id),
    PRIMARY KEY (tracker, category_id, position)
);
CREATE INDEX catalog_entries_release ON catalog_entries(release_id);

-- Разделы трекеров: дерево для настроек (этап 7) и названия в каталоге.
CREATE TABLE categories (
    tracker   TEXT NOT NULL,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    parent_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tracker, id)
);

-- Обновление разделов: сколько раздач пришло в прошлый раз и когда — удачно.
CREATE TABLE catalog_state (
    tracker      TEXT NOT NULL,
    category_id  TEXT NOT NULL,
    last_count   INTEGER NOT NULL DEFAULT 0,
    refreshed_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tracker, category_id)
);
