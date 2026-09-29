-- История поиска (этап 7): последние запросы, общие для пульта и телевизоров. Ключ — запрос без
-- учёта регистра и лишних пробелов; query — как его набрали в последний раз. Время — мс Unix.
CREATE TABLE search_history (
    key   TEXT PRIMARY KEY,
    query TEXT NOT NULL,
    at    INTEGER NOT NULL
);
