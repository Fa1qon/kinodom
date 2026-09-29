-- IPTV (спека этапа 8, раздел 5.10). Время — мс Unix. Справочник каналов и передачи — в памяти из
-- файла телепрограммы; сопоставление потоков с каналами считается в памяти и сюда не пишется.

-- Плейлисты: по ссылке (url) или файлом (url = '').
CREATE TABLE iptv_playlists (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    url         TEXT NOT NULL DEFAULT '',
    limited     INTEGER NOT NULL DEFAULT 0, -- «ограничено число просмотров»: в фоне не проверяется
    added_at    INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0, -- последнее удачное обновление
    tried_at    INTEGER NOT NULL DEFAULT 0, -- последняя попытка
    error       TEXT NOT NULL DEFAULT '',
    unsupported INTEGER NOT NULL DEFAULT 0  -- записей с неподдерживаемыми ссылками (udp, rtmp…)
);

-- Источники: одна ссылка — один источник, сколько бы плейлистов её ни содержали.
CREATE TABLE iptv_streams (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    url      TEXT NOT NULL UNIQUE,
    kind     TEXT NOT NULL DEFAULT '',    -- hls, dash, live; '' — ещё не известен
    quality  TEXT NOT NULL DEFAULT '',    -- по проверке (RESOLUTION); '' — по названию
    state    TEXT NOT NULL DEFAULT 'new', -- new, alive, silent, dead
    fails    INTEGER NOT NULL DEFAULT 0,  -- «не отвечает» подряд
    grade    TEXT NOT NULL DEFAULT '',    -- последняя полная проверка: green, yellow, red; '' — не было
    light_at INTEGER NOT NULL DEFAULT 0,
    full_at  INTEGER NOT NULL DEFAULT 0,
    ttfb_ms  INTEGER NOT NULL DEFAULT 0,
    ratio    REAL NOT NULL DEFAULT 0,
    mbps     REAL NOT NULL DEFAULT 0,
    error    TEXT NOT NULL DEFAULT ''
);

-- Записи плейлистов: как источник подписан в каждом плейлисте.
CREATE TABLE iptv_entries (
    playlist_id INTEGER NOT NULL REFERENCES iptv_playlists(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    stream_id   INTEGER NOT NULL REFERENCES iptv_streams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    tvg_id      TEXT NOT NULL,
    tvg_name    TEXT NOT NULL,
    tvg_shift   INTEGER NOT NULL,
    logo        TEXT NOT NULL,
    grp         TEXT NOT NULL,
    user_agent  TEXT NOT NULL,
    referrer    TEXT NOT NULL,
    PRIMARY KEY (playlist_id, position)
);
CREATE INDEX iptv_entries_stream ON iptv_entries(stream_id);

-- Ручные правки сопоставления: по ссылке («это другой канал») и по нормализованному названию
-- (назначение из «Не распознано»). channel = '' и hidden = 1 — скрыт.
CREATE TABLE iptv_stream_rules (
    url     TEXT PRIMARY KEY,
    channel TEXT NOT NULL,
    hidden  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE iptv_name_rules (
    name    TEXT PRIMARY KEY,
    channel TEXT NOT NULL,
    hidden  INTEGER NOT NULL DEFAULT 0
);

-- Правки канала: NULL — не правили.
CREATE TABLE iptv_channel_overrides (
    channel    TEXT PRIMARY KEY,
    hidden     INTEGER NOT NULL DEFAULT 0,
    category   TEXT,
    country    TEXT,
    languages  TEXT, -- через запятую
    pinned_url TEXT NOT NULL DEFAULT ''
);

-- Избранное — у каждого устройства своё (спека этапа 8, раздел 4).
CREATE TABLE iptv_favorites (
    device   TEXT NOT NULL,
    channel  TEXT NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (device, channel)
);

-- Результаты проверок за 7 дней: порядок источников и «днём / вечером» в карточке канала.
CREATE TABLE probe_results (
    stream_id INTEGER NOT NULL REFERENCES iptv_streams(id) ON DELETE CASCADE,
    at        INTEGER NOT NULL,
    level     TEXT NOT NULL, -- light, full
    grade     TEXT NOT NULL, -- alive, green, yellow, red, black
    ratio     REAL NOT NULL,
    ttfb_ms   INTEGER NOT NULL,
    mbps      REAL NOT NULL,
    error     TEXT NOT NULL
);
CREATE INDEX probe_results_stream ON probe_results(stream_id, at);
CREATE INDEX probe_results_at ON probe_results(at);
