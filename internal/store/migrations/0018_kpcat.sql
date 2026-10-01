-- Каталог «Кинопоиск» (план 14Г): фильмы популярных списков сайта, места в разделах, время обновления.
CREATE TABLE kpcat_films (
    kp_id      INTEGER PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT '',
    name_ru    TEXT NOT NULL DEFAULT '',
    name_orig  TEXT NOT NULL DEFAULT '',
    year       INTEGER NOT NULL DEFAULT 0,
    rating     REAL NOT NULL DEFAULT 0,
    votes      INTEGER NOT NULL DEFAULT 0,
    premiere   INTEGER NOT NULL DEFAULT 0, -- мс; 0 — дата выхода неизвестна
    poster     TEXT NOT NULL DEFAULT '',   -- адрес аватара Кинопоиска
    genres     TEXT NOT NULL DEFAULT '',   -- через «, »
    countries  TEXT NOT NULL DEFAULT '',
    imdb       REAL NOT NULL DEFAULT 0,    -- 0 — нет у сайта или ещё не спрашивали
    imdb_votes INTEGER NOT NULL DEFAULT 0,
    imdb_at    INTEGER NOT NULL DEFAULT 0, -- когда спрашивали IMDb (мс); 0 — не спрашивали
    updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE kpcat_entries (
    section  TEXT NOT NULL,
    position INTEGER NOT NULL,
    kp_id    INTEGER NOT NULL REFERENCES kpcat_films(kp_id),
    PRIMARY KEY (section, position)
);
CREATE INDEX kpcat_entries_film ON kpcat_entries(kp_id);

CREATE TABLE kpcat_state (
    section      TEXT PRIMARY KEY,
    refreshed_at INTEGER NOT NULL,
    total        INTEGER NOT NULL
);
