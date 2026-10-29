-- Журнал вкуса для рекомендаций (план рекомендаций, ревью журнала 2026-10-29). Время — мс Unix.
--
-- watch_progress отвечает на «где остановились» на устройстве и перезаписывает место; рекомендациям нужно
-- другое: кто смотрел (профиль семьи, иначе устройство), сколько реально посмотрел (а не куда перемотал),
-- сколько раз садился смотреть и чем кончилось — досмотрел, бросил, удалил сам, убрала уборка.
CREATE TABLE view_log (
    owner        TEXT NOT NULL,              -- «p:<профиль>» или «d:<устройство>»
    hash         TEXT NOT NULL,              -- infohash или lib-<единица>, как в watch_progress
    file_index   INTEGER NOT NULL,
    kp_id        INTEGER NOT NULL DEFAULT 0, -- фильм; 0 — пока не опознан
    first_at     INTEGER NOT NULL,
    last_at      INTEGER NOT NULL,
    sessions     INTEGER NOT NULL DEFAULT 1, -- садился смотреть (перерыв больше 30 минут — новый раз)
    seen         REAL NOT NULL DEFAULT 0,    -- доля файла, просмотренная подряд, без перемоток вперёд
    max_fraction REAL NOT NULL DEFAULT 0,    -- самое дальнее место
    last_fraction REAL NOT NULL DEFAULT 0,   -- где остановился в последний раз
    finished     INTEGER NOT NULL DEFAULT 0, -- досмотрел: дошёл до 90 % и видел не меньше половины
    times        INTEGER NOT NULL DEFAULT 0, -- сколько раз досмотрел (пересмотры)
    ended        TEXT NOT NULL DEFAULT '',   -- '' | deleted (удалил сам) | expired (уборка по сроку) | removed (убрал из истории)
    ended_at     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (owner, hash, file_index)
);
CREATE INDEX view_log_owner ON view_log(owner, last_at);
CREATE INDEX view_log_hash ON view_log(hash);

-- Оценки 1–10 и «не моё» / «видел» — по человеку и фильму Кинопоиска.
CREATE TABLE rec_ratings (
    owner  TEXT NOT NULL,
    kp_id  INTEGER NOT NULL,
    score  INTEGER NOT NULL CHECK (score BETWEEN 1 AND 10),
    source TEXT NOT NULL,   -- kinodom | kp (импорт) | quiz
    at     INTEGER NOT NULL,
    PRIMARY KEY (owner, kp_id)
);
CREATE TABLE rec_marks (
    owner TEXT NOT NULL,
    kp_id INTEGER NOT NULL,
    mark  TEXT NOT NULL,    -- notmine | seen | want
    at    INTEGER NOT NULL,
    PRIMARY KEY (owner, kp_id)
);
-- Показанные рекомендации: не повторять одно и то же каждый вечер.
CREATE TABLE rec_shown (
    owner TEXT NOT NULL,
    kp_id INTEGER NOT NULL,
    at    INTEGER NOT NULL,
    PRIMARY KEY (owner, kp_id)
);
