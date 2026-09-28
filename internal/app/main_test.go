package app

import (
	"testing"

	"kinodom/internal/offlinetest"
)

// Тесты пакета не ходят в интернет: встроенные адреса трекеров и Кинопоиска — на растяжке.
func TestMain(m *testing.M) { offlinetest.Main(m) }
