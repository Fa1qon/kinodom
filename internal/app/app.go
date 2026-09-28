// Package app собирает сервер из модулей. Его используют команда run, служба (этап 11)
// и интеграционные тесты — поэтому «все модули вместе» везде собираются одинаково.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"kinodom/internal/api"
	"kinodom/internal/config"
	"kinodom/internal/logx"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/web"
)

type Options struct {
	Home       string // корневая папка; пусто — config.DefaultHome()
	Console    bool   // дублировать журнал в консоль
	ListenAddr string // адрес API; пусто — ":<apiPort>" из kinodom.json
}

type App struct {
	Paths config.Paths
	Boot  config.Bootstrap
	Log   *slog.Logger
	DB    *store.DB
	Sup   *supervisor.Supervisor
	API   *api.Server

	closers []io.Closer // закрываются в обратном порядке
}

func New(ctx context.Context, o Options) (*App, error) {
	home := o.Home
	if home == "" {
		home = config.DefaultHome()
	}
	a := &App{Paths: config.NewPaths(home)}
	if err := a.Paths.Ensure(); err != nil {
		return nil, fmt.Errorf("папки Kinodom: %w", err)
	}
	boot, err := config.LoadBootstrap(a.Paths.Bootstrap)
	if err != nil {
		return nil, err
	}
	a.Boot = boot
	log, logCloser, err := logx.New(a.Paths.Logs, o.Console)
	if err != nil {
		return nil, fmt.Errorf("журнал: %w", err)
	}
	a.Log = log
	a.closers = append(a.closers, logCloser)
	db, err := store.Open(ctx, a.Paths.DB)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("база: %w", err)
	}
	a.DB = db
	a.closers = append(a.closers, db)

	a.Sup = supervisor.New(log, supervisor.WithErrorSink(func(module, text string) {
		if err := db.AddError(context.Background(), module, text); err != nil {
			log.Error("не удалось записать ошибку модуля", "err", err)
		}
	}))
	addr := o.ListenAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", boot.APIPort)
	}
	a.API = api.New(addr, api.Deps{Log: log, DB: db, Sup: a.Sup, Web: web.Static})
	a.Sup.Add(a.API, true) // API выключать нельзя: без него нет ни пульта, ни телевизоров
	// Следующие этапы добавляют сюда свои модули: a.Sup.Add(m, a.ModuleEnabled(ctx, m.Name())).
	return a, nil
}

// ModuleEnabled — модуль включён, если в настройках нет modules.<имя>.enabled = "false".
func (a *App) ModuleEnabled(ctx context.Context, name string) bool {
	v, ok, err := a.DB.Setting(ctx, "modules."+name+".enabled")
	if err != nil || !ok {
		return true
	}
	return v != "false"
}

// Run работает до отмены ctx.
func (a *App) Run(ctx context.Context) {
	a.Log.Info("Kinodom запущен", "home", a.Paths.Home)
	a.Sup.Run(ctx)
	a.Log.Info("Kinodom остановлен")
}

func (a *App) Close() error {
	var errs []error
	for i := len(a.closers) - 1; i >= 0; i-- {
		errs = append(errs, a.closers[i].Close())
	}
	a.closers = nil
	return errors.Join(errs...)
}
