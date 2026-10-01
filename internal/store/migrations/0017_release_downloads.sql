-- Сколько раз скачана раздача (план 14Б): t2 — поиск форума и страница раздачи; 0 — неизвестно.
ALTER TABLE releases ADD COLUMN downloads INTEGER NOT NULL DEFAULT 0;
