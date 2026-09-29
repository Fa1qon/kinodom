-- Раздачи одного фильма (спека этапа 7, раздел 10.4): по номеру Кинопоиска из описания раздачи и по
-- найденному очередью рейтингов.
CREATE INDEX releases_kinopoisk ON releases(kinopoisk_id);
CREATE INDEX kp_releases_kp ON kp_releases(kp_id);
