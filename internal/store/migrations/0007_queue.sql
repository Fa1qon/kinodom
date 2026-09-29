-- Очередь загрузки (этап 7): «Скачать» до получения списка файлов (раздача снова открывается после
-- перезапуска и ждёт метаинфо) и файл в фокусе — тот, что качается сейчас; −1 — никакой.
ALTER TABLE torrents ADD COLUMN download_all INTEGER NOT NULL DEFAULT 0;
ALTER TABLE torrents ADD COLUMN focus_file INTEGER NOT NULL DEFAULT -1;
