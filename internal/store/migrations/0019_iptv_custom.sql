-- Свои каналы (план 14Д): «Новый канал» из «Не распознано» — ключ my-N, название, логотип.
CREATE TABLE iptv_custom (
    key        TEXT PRIMARY KEY,
    n          INTEGER NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    logo       TEXT NOT NULL DEFAULT '', -- адрес картинки; upload:<ключ> — загруженный файл
    created_at INTEGER NOT NULL
);
