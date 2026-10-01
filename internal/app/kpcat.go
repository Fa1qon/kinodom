package app

import (
	"context"

	"kinodom/internal/kpcat"
	"kinodom/internal/meta"
)

// initKPCat — каталог «Кинопоиск» (план 14Г): списки сайта без ключа через общие ворота Кинопоиска, постеры —
// в кэш картинок напрямую.
func (a *App) initKPCat(ctx context.Context) {
	o := kpcat.Options{DB: a.DB, KP: a.kpweb, Log: a.Log.With("module", "kpcat")}
	if a.Images != nil {
		o.Posters = func(ctx context.Context, src string) (string, error) { return a.Images.Fetch(ctx, src, meta.Direct) }
	}
	a.KPCat = kpcat.New(o)
	a.KPCat.Register(a.API)
	a.Sup.Add(a.KPCat, a.ModuleEnabled(ctx, a.KPCat.Name()))
}
