-- Кинопоиск: модуль ratings (спека, раздел 8). Время — миллисекунды Unix.

-- Фильмы: найденный фильм хранится всегда, рейтинг обновляется раз в 30 дней.
CREATE TABLE kp_films (
    kp_id       INTEGER PRIMARY KEY,
    imdb_id     TEXT NOT NULL DEFAULT '',
    name_ru     TEXT NOT NULL DEFAULT '',
    name_orig   TEXT NOT NULL DEFAULT '',
    year        INTEGER NOT NULL DEFAULT 0,
    type        TEXT NOT NULL DEFAULT '',
    rating      REAL NOT NULL DEFAULT 0,     -- ratingKinopoisk; 0 — рейтинга нет
    rating_imdb REAL NOT NULL DEFAULT 0,
    rating_at   INTEGER NOT NULL DEFAULT 0   -- когда обновлён рейтинг
);

-- Поиск по названию: (нормализованное название, год) → фильм. Разные рипы одного фильма не
-- тратят запросы. kp_id NULL — не найдено, до retry_at не искать.
CREATE TABLE kp_titles (
    title    TEXT NOT NULL,
    year     INTEGER NOT NULL,
    kp_id    INTEGER,
    retry_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (title, year)
);

-- Раздача («rutor:1077013») → фильм. kp_id NULL — не найдено, до retry_at не искать.
CREATE TABLE kp_releases (
    release_id TEXT PRIMARY KEY,
    kp_id      INTEGER,
    retry_at   INTEGER NOT NULL DEFAULT 0
);

-- Очередь: что узнать. Меньший prio — раньше: квота тратится в порядке основного каталога.
CREATE TABLE kp_queue (
    release_id   TEXT PRIMARY KEY,
    prio         INTEGER NOT NULL,
    kinopoisk_id INTEGER NOT NULL DEFAULT 0,
    imdb_id      TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    not_before   INTEGER NOT NULL DEFAULT 0  -- после временной ошибки — не раньше
);
CREATE INDEX kp_queue_prio ON kp_queue(prio);
