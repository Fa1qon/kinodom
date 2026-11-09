package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"kinodom/internal/settings"
	"kinodom/internal/source/cardigann"
	"kinodom/internal/trackerpack"
)

// trackerProfiles — профили трекеров (trackers.pack, docs/data-packs-and-updates.md, раздел 3): программа
// трекеров не знает, по адресу из настроек профиль говорит, каким разбором его читать. Подменяется на ходу,
// когда новый файл приходит обновлением баз.
type trackerProfiles struct {
	p atomic.Pointer[trackerpack.Pack]
}

func (t *trackerProfiles) Lookup(addr string) (trackerpack.Profile, error) {
	if p := t.p.Load(); p != nil {
		return p.Lookup(addr)
	}
	return trackerpack.Profile{}, trackerpack.ErrUnknown
}

// loadTrackerPack — файл из папки данных (пришёл обновлением), иначе из установщика рядом с программой.
func (a *App) loadTrackerPack() {
	paths := []string{filepath.Join(a.Paths.Data, "trackers.pack")}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "trackers.pack"))
	}
	for _, path := range paths {
		p, err := trackerpack.Load(path)
		if err != nil {
			a.Log.Warn("профили трекеров не читаются", "path", path, "err", err)
			continue
		}
		if p.Len() > 0 {
			a.profiles.p.Store(p)
			return
		}
	}
	empty, _ := trackerpack.Load("")
	a.profiles.p.Store(empty)
}

// installTrackerPack — новый файл профилей из обновления баз: копия в папку данных и подмена.
func (a *App) installTrackerPack(ctx context.Context, path string) error {
	p, err := trackerpack.Load(path)
	if err != nil {
		return err
	}
	if p.Len() == 0 {
		return fmt.Errorf("профили трекеров пусты")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dst := filepath.Join(a.Paths.Data, "trackers.pack")
	if err := os.WriteFile(dst+".new", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(dst+".new", dst); err != nil {
		return err
	}
	a.profiles.p.Store(p)
	return nil
}

// migrateOpenTrackers — установки, где открытые трекеры включались сами (до списка «Трекеры»): уже скачанные
// описания остаются подключены по своим адресам. Новая установка начинает с пустого списка.
func (a *App) migrateOpenTrackers(ctx context.Context, v *settings.Values) {
	if v.OpenTrackers != nil {
		return
	}
	v.OpenTrackers = []settings.OpenTracker{}
	for _, t := range cardigann.Downloaded(filepath.Join(a.Paths.Data, "trackers")) {
		v.OpenTrackers = append(v.OpenTrackers, settings.OpenTracker{Def: t.Def, Address: t.Address})
	}
	b, _ := json.Marshal(v.OpenTrackers)
	if err := a.DB.SetSetting(ctx, settings.KeyOpenTrackers, string(b)); err != nil {
		a.Log.Warn("список трекеров не сохранился", "err", err)
	}
}

func poolTrackers(l []settings.OpenTracker) []cardigann.Tracker {
	out := make([]cardigann.Tracker, 0, len(l))
	for _, t := range l {
		out = append(out, cardigann.Tracker{Def: t.Def, Address: t.Address})
	}
	return out
}

// trackerNames — названия подключённых трекеров для пульта: со своим разбором — из профиля адреса, по описаниям —
// из описания. Неподключённых в списке нет: пульт сам названий не знает.
func (a *App) trackerNames() map[string]string {
	out := map[string]string{}
	v := a.Settings.Current()
	for id, addr := range map[string]string{"t2": v.t2Address, "t1": v.t1Address} {
		if addr == "" {
			continue
		}
		name, _ := trackerpack.Domain(addr)
		if p, err := a.profiles.Lookup(addr); err == nil {
			name = p.Name
		}
		out[id] = name
	}
	for id, name := range a.trackers.Trackers() {
		out[id] = name
	}
	return out
}
