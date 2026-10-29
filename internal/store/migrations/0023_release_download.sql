-- Ссылка на .torrent раздачи из поиска по трекерам из описаний Jackett (Kinodom 0.17): magnet в их выдаче
-- нет — файл скачивается, когда раздачу открыли. '' — нет ссылки.
ALTER TABLE releases ADD COLUMN download_url TEXT NOT NULL DEFAULT '';
