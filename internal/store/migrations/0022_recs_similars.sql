-- Сериалы и русское кино в рекомендациях (0021 уже стоит у тех, кто ставил проверочные сборки, —
-- новое только здесь).

-- Карточка-заготовка из «Похожих»: только название и постер, вид и рейтинг дозапрашиваются.
ALTER TABLE rec_cards ADD COLUMN full INTEGER NOT NULL DEFAULT 1;
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
