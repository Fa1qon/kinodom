-- Подписка на новые серии (спека 11b, раздел 6). Время — мс Unix.

-- Раздачи, за которыми следят: известная версия (infohash) и сколько серий в ней.
CREATE TABLE follows (
    release_id INTEGER PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
    state      TEXT NOT NULL DEFAULT 'active', -- active, finished (вышла и скачана последняя), removed (снята с трекера)
    infohash   TEXT NOT NULL,                  -- версия, которую видели последней
    episodes   INTEGER NOT NULL DEFAULT 0,     -- вышло серий
    total      INTEGER NOT NULL DEFAULT 0,     -- «из N»; 0 — неизвестно
    checked_at INTEGER NOT NULL DEFAULT 0,     -- последняя проверка страницы раздачи
    created_at INTEGER NOT NULL
);

-- Оповещения «Новые серии» (общие для семьи).
CREATE TABLE updates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL DEFAULT 'episodes', -- episodes, removed (раздача снята с трекера)
    infohash   TEXT NOT NULL DEFAULT '',         -- версия, в которой вышли серии
    files      TEXT NOT NULL DEFAULT '[]',       -- JSON [{index, path}] новых серий
    label      TEXT NOT NULL DEFAULT '',         -- «1×07–1×08»
    at         INTEGER NOT NULL,
    dismissed  INTEGER NOT NULL DEFAULT 0        -- «Убрать»
);
CREATE INDEX updates_release ON updates(release_id);

-- Переход скачанной раздачи на обновлённую версию (модуль torrents): пометка «идёт переход» — при сбое
-- посередине переход доводится при старте, иначе скачанное качалось бы заново (спека 11b, 6.3.4).
CREATE TABLE upgrades (
    old        TEXT PRIMARY KEY, -- infohash прежней версии
    new        TEXT NOT NULL,    -- infohash новой
    moves      TEXT NOT NULL,    -- JSON [{from, to}] — абсолютные пути файлов
    state      TEXT NOT NULL,    -- db (база переписана, файлы ещё переносятся), moved (перенесены, ждут проверки)
    started_at INTEGER NOT NULL
);
