package edge

import (
	"context"
	"log/slog"

	"kinodom/internal/supervisor"
)

// Module — модуль «edge» (спека, раздел 3): скрытый Edge под сторожем. Пропуск добывает Fetcher
// по требованию источника; модуль при старте привязывает дочерние процессы к kinodom — Edge не
// переживёт аварию, даже если первый проход случится не скоро, — и виден в «Состоянии».
// Выключенный модуль — Rutracker без пропуска (только API).
type Module struct{ log *slog.Logger }

func NewModule(log *slog.Logger) *Module {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Module{log: log}
}

func (m *Module) Name() string { return "edge" }

func (m *Module) Run(ctx context.Context) error {
	if err := BindChildren(); err != nil {
		m.log.Warn("Edge: не удалось привязать к процессу kinodom — после аварии Edge может остаться", "err", err)
	}
	supervisor.Ready(ctx)
	<-ctx.Done()
	return nil
}
