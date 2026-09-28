-- Общие таблицы сервера. Время — миллисекунды Unix.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE problems (
    id    TEXT PRIMARY KEY,
    text  TEXT NOT NULL,
    since INTEGER NOT NULL
);

CREATE TABLE errors (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    at     INTEGER NOT NULL,
    module TEXT NOT NULL,
    text   TEXT NOT NULL
);
