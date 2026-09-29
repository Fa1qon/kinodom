-- История просмотров по устройствам (спека этапа 8, раздел 7). Время — мс Unix.
CREATE TABLE watch_progress (
    device       TEXT NOT NULL,    -- адрес устройства в домашней сети; «pc» — этот ПК
    hash         TEXT NOT NULL,    -- infohash раздачи (нижний регистр)
    file_index   INTEGER NOT NULL,
    fraction     REAL NOT NULL,    -- где остановились: доля файла от 0 до 1
    position_sec REAL NOT NULL,    -- где остановились, с; 0 — неизвестно (нет длительности)
    duration_sec REAL NOT NULL,    -- длительность файла, с; 0 — неизвестна
    watched      INTEGER NOT NULL, -- просмотрен: дошли до 90 % или отметили в пульте
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (device, hash, file_index)
);
CREATE INDEX watch_progress_recent ON watch_progress(device, updated_at);

-- Длительность видеофайлов раздач: из заголовка файла или от своего плеера.
CREATE TABLE media_durations (
    hash         TEXT NOT NULL,
    file_index   INTEGER NOT NULL,
    duration_sec REAL NOT NULL,
    PRIMARY KEY (hash, file_index)
);
