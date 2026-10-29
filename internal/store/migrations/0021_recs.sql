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
-- Карточки фильмов, которых нет в пакете модели целиком (пакет несёт карточки популярных): дозапрошены
-- с Кинопоиска, когда фильм попал в подборку. missing = 1 — Кинопоиск не отдал, не спрашивать до fetched_at + неделя.
CREATE TABLE rec_cards (
    kp_id       INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    title_orig  TEXT NOT NULL DEFAULT '',
    year        INTEGER NOT NULL DEFAULT 0,
    genres      TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    poster      TEXT NOT NULL DEFAULT '',
    rating_kp   REAL NOT NULL DEFAULT 0,
    rating_imdb REAL NOT NULL DEFAULT 0,
    missing     INTEGER NOT NULL DEFAULT 0,
    full        INTEGER NOT NULL DEFAULT 1, -- 0 — только название и постер из «Похожих»: вид и рейтинг дозапросить
    fetched_at  INTEGER NOT NULL
);

-- Прежняя история — в журнал вкуса, чтобы подборка с первого дня учитывала, что уже смотрели. Перемоток
-- в ней не видно: место остановки считается просмотренным подряд. Хозяин — профиль, на который сейчас
-- переключено устройство, иначе устройство; фильм журнал узнаёт сам при первом чтении (ResolveFilms).
INSERT INTO view_log (owner, hash, file_index, kp_id, first_at, last_at, sessions, seen, max_fraction, last_fraction, finished, times)
SELECT COALESCE('p:' || d.profile_id, 'd:' || w.device), w.hash, w.file_index, 0, w.updated_at, w.updated_at, 1,
       w.fraction, MAX(w.fraction, CASE WHEN w.watched THEN 1 ELSE 0 END), w.fraction, w.watched, w.watched
FROM watch_progress w LEFT JOIN family_devices d ON d.device = w.device
WHERE w.device != ''
ON CONFLICT(owner, hash, file_index) DO NOTHING;

-- «Похожие» Кинопоиска для фильмов профиля, у которых нет связей в пакете (сериалы, русское кино):
-- запрашиваются по ключу Кинопоиска понемногу (общий суточный лимит с рейтингами). rank — место в списке.
CREATE TABLE rec_similars (
    kp_id    INTEGER NOT NULL,
    other_kp INTEGER NOT NULL,
    rank     INTEGER NOT NULL,
    PRIMARY KEY (kp_id, other_kp)
);
-- Чьи «Похожие» уже спрашивали (и когда): не спрашивать снова месяц.
CREATE TABLE rec_similars_state (
    kp_id      INTEGER PRIMARY KEY,
    fetched_at INTEGER NOT NULL
);
