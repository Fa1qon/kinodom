-- Звук источника и скрытые у канала источники (отзыв заказчика 2026-09-30).
ALTER TABLE iptv_streams ADD COLUMN audio INTEGER; -- 1 — есть звук, 0 — нет, NULL — не знаем
ALTER TABLE iptv_channel_overrides ADD COLUMN hidden_urls TEXT NOT NULL DEFAULT ''; -- ссылки через перевод строки
