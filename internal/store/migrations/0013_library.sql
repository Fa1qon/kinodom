-- Медиатека (спека этапа 9, раздел 5.9). Время — мс Unix.
CREATE TABLE lib_categories (
    id        INTEGER PRIMARY KEY,
    name      TEXT NOT NULL,
    layout    TEXT NOT NULL,             -- films — «как фильмы», series — «как сериалы»
    kinopoisk INTEGER NOT NULL DEFAULT 0,
    hidden    INTEGER NOT NULL DEFAULT 0,
    builtin   TEXT NOT NULL DEFAULT '',  -- films, series — стандартные; '' — своя
    position  INTEGER NOT NULL DEFAULT 0
);
INSERT INTO lib_categories (id, name, layout, kinopoisk, builtin, position) VALUES
    (1, 'Фильмы', 'films', 1, 'films', 1),
    (2, 'Сериалы', 'series', 1, 'series', 2);

CREATE TABLE lib_folders (
    id       INTEGER PRIMARY KEY,
    category INTEGER NOT NULL REFERENCES lib_categories(id) ON DELETE CASCADE,
    path     TEXT NOT NULL,
    path_key TEXT NOT NULL UNIQUE        -- путь в нижнем регистре без «\» на конце: одна папка — одна категория
);

-- Скрытые категории, которые показываются на устройстве (адрес в домашней сети; «pc» — этот ПК).
CREATE TABLE lib_devices (
    device   TEXT NOT NULL,
    category INTEGER NOT NULL REFERENCES lib_categories(id) ON DELETE CASCADE,
    PRIMARY KEY (device, category)
);

-- AUTOINCREMENT у единиц и файлов: номер не выдаётся повторно — на номерах держится история
-- просмотров (lib-<единица>, номер файла), новый фильм не должен унаследовать место удалённого.
CREATE TABLE lib_units (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source       TEXT NOT NULL,          -- folder, torrent
    key          TEXT NOT NULL UNIQUE,   -- полный путь папки или файла / infohash
    folder       INTEGER REFERENCES lib_folders(id) ON DELETE CASCADE, -- NULL у раздачи
    name         TEXT NOT NULL DEFAULT '', -- имя папки или файла; у раздачи — её название
    title        TEXT NOT NULL DEFAULT '', -- разобранное название
    year         INTEGER NOT NULL DEFAULT 0,
    kp_id        INTEGER NOT NULL DEFAULT 0,
    state        TEXT NOT NULL DEFAULT 'new', -- new, found, linked (ссылка из пульта), unrecognized, manual, wait, plain
    attempts     INTEGER NOT NULL DEFAULT 0,  -- неудачных поисков подряд (Кинопоиск сбоит)
    search_at    INTEGER NOT NULL DEFAULT 0,  -- ждёт повтора поиска не раньше; 0 — сразу
    manual_title TEXT NOT NULL DEFAULT '',
    manual_year  INTEGER NOT NULL DEFAULT 0,
    missing      INTEGER NOT NULL DEFAULT 0, -- папка категории недоступна: единица не показывается
    added_at     INTEGER NOT NULL
);
CREATE INDEX lib_units_kp ON lib_units(kp_id);

CREATE TABLE lib_files (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    unit     INTEGER NOT NULL REFERENCES lib_units(id) ON DELETE CASCADE,
    path     TEXT NOT NULL,              -- полный путь (папки) или путь внутри раздачи
    tindex   INTEGER NOT NULL DEFAULT -1, -- номер файла раздачи; -1 у файла из папки
    season   INTEGER NOT NULL DEFAULT 0,
    section  TEXT NOT NULL DEFAULT '',   -- раздел без номера сезона (глава курса)
    episode  INTEGER NOT NULL DEFAULT 0,
    position INTEGER NOT NULL DEFAULT 0, -- порядок показа в единице
    size     INTEGER NOT NULL DEFAULT 0,
    mtime    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (unit, path)
);

CREATE TABLE lib_cards (
    key         TEXT PRIMARY KEY,        -- kp-<номер Кинопоиска>, u-<единица>
    title       TEXT NOT NULL DEFAULT '',
    name_orig   TEXT NOT NULL DEFAULT '',
    year        INTEGER NOT NULL DEFAULT 0,
    type        TEXT NOT NULL DEFAULT '', -- FILM, TV_SERIES, MINI_SERIES, TV_SHOW, VIDEO; '' — неизвестен
    genres      TEXT NOT NULL DEFAULT '', -- через запятую
    description TEXT NOT NULL DEFAULT '',
    image_key   TEXT NOT NULL DEFAULT '', -- постер в кэше картинок (/img/{key})
    source      TEXT NOT NULL DEFAULT '', -- release, kinopoisk, manual
    category    INTEGER,                 -- ручной перенос; NULL — категория по умолчанию
    fetched_at  INTEGER NOT NULL DEFAULT 0
);
