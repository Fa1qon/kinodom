-- Сессия трекера между перезапусками службы (спека этапа 11a, раздел 8): пропуск Cloudflare и вход
-- Rutracker. База лежит в data\ — её читают только служба и администраторы.
CREATE TABLE tracker_state (
    tracker  TEXT PRIMARY KEY,
    session  TEXT NOT NULL,  -- JSON: зеркало, User-Agent, cookie (имя и значение)
    saved_at INTEGER NOT NULL -- мс Unix
);
