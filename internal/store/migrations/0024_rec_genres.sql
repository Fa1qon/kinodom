-- Жанры, заданные человеком в «Моём вкусе» (2026-11-08): 1 — люблю (подборка поднимает), -1 — не смотрю
-- (подборка опускает, пока жанр не выбран кнопкой явно).
CREATE TABLE rec_genres (
    owner TEXT NOT NULL,
    genre TEXT NOT NULL,   -- жанр Кинопоиска в нижнем регистре: «фантастика»
    pref  INTEGER NOT NULL CHECK (pref IN (-1, 1)),
    at    INTEGER NOT NULL,
    PRIMARY KEY (owner, genre)
);
