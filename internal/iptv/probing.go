package iptv

import (
	"context"
	"slices"
	"sync"
	"time"

	"kinodom/internal/iptv/probe"
	"kinodom/internal/supervisor"
)

// nextAt — ближайшее время после now, когда часы (по местному времени now) совпадают с одним из hours.
func nextAt(now time.Time, hours []int) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for d := 0; d < 2; d++ {
		for _, h := range hours {
			t := day.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour)
			if t.After(now) {
				return t
			}
		}
	}
	return day.AddDate(0, 0, 2)
}

// lightLoop — лёгкая проверка: новые источники сразу, все — раз в сутки в 4:00.
func (m *Module) lightLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if ids := m.newStreams(); len(ids) > 0 {
			m.runLight(ctx, ids)
			// Каналы, которые только что появились, сразу получают оценку: полная проверка их
			// источников, если её ещё не было.
			m.rebuild(ctx)
			m.runFull(ctx, m.fullTargets(true, m.favoriteKeys(ctx)))
			continue
		}
		wait := time.Until(nextAt(m.local(), []int{lightHour}))
		select {
		case <-ctx.Done():
			return nil
		case <-m.lightNew:
		case <-time.After(wait):
			m.runLight(ctx, m.lightTargets())
		}
	}
}

// fullLoop — полная проверка по часам fullHours.
func (m *Module) fullLoop(ctx context.Context) error {
	for {
		wait := time.Until(nextAt(m.local(), fullHours))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
			m.runFull(ctx, m.fullTargets(false, m.favoriteKeys(ctx)))
		}
	}
}

// limitedOnly — источник есть только в «ограниченных» плейлистах: в фоне не проверяется.
func (m *Module) limitedOnly(s *Stream) bool {
	for _, e := range s.Entries {
		if pl := m.pool.playlists[e.Playlist]; pl == nil || !pl.Limited {
			return false
		}
	}
	return len(s.Entries) > 0
}

func (m *Module) newStreams() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for _, s := range m.pool.streams {
		if s.State == StateNew && !m.limitedOnly(s) {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// lightTargets — весь пул, кроме «ограниченных».
func (m *Module) lightTargets() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for _, s := range m.pool.streams {
		if !m.limitedOnly(s) {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// favoriteKeys — каналы в избранном хоть одного устройства: их проверяем, даже если категория скрыта.
func (m *Module) favoriteKeys(ctx context.Context) map[string]bool {
	keys, err := m.d.allFavorites(ctx)
	if err != nil {
		m.log.Warn("iptv: избранное не читается", "err", err)
	}
	return keys
}

// fullTargets — у каждого видимого канала (или в избранном хоть одного устройства): первые
// fullPerChannel предлагаемых источников, закреплённый и молчащие — иначе канал, у которого источник
// раз не ответил, пропал бы с экрана до лёгкой проверки в 4:00. Мёртвые и «ограниченные» — нет.
// onlyNew — только те, у кого полной проверки ещё не было.
func (m *Module) fullTargets(onlyNew bool, favorites map[string]bool) []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[int64]bool{}
	var ids []int64
	add := func(s *Stream) {
		if seen[s.ID] || s.State == StateDead || m.limitedOnly(s) || (onlyNew && !s.FullAt.IsZero()) {
			return
		}
		seen[s.ID] = true
		ids = append(ids, s.ID)
	}
	keys := make([]string, 0, len(m.lineup.ByKey))
	for k := range m.lineup.ByKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		c := m.lineup.ByKey[k]
		if c.Hidden != "" && !favorites[k] {
			continue
		}
		for i, s := range c.Sources {
			if i < fullPerChannel || s.URL == c.Pinned {
				add(s)
			}
		}
		for _, s := range c.Others {
			if s.State == StateSilent {
				add(s)
			}
		}
	}
	return ids
}

// runLight / runFull — проход проверок; ход виден в пульте. Проход не начинается, пока идёт
// предыдущий той же ступени.
func (m *Module) runLight(ctx context.Context, ids []int64) {
	m.run(ctx, ids, "light", lightParallel, &m.light)
}

func (m *Module) runFull(ctx context.Context, ids []int64) {
	m.run(ctx, ids, "full", fullParallel, &m.full)
}

func (m *Module) run(ctx context.Context, ids []int64, level string, parallel int, pr *Progress) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	if pr.Running {
		m.mu.Unlock()
		return
	}
	*pr = Progress{Running: true, Total: len(ids), Finished: pr.Finished}
	m.mu.Unlock()
	m.check(ctx, ids, level, parallel, func() {
		m.mu.Lock()
		pr.Done++
		m.mu.Unlock()
	})
	m.mu.Lock()
	pr.Running, pr.Finished = false, m.now()
	m.mu.Unlock()
	m.changed()
}

// check — проверить источники не больше parallel одновременно.
func (m *Module) check(ctx context.Context, ids []int64, level string, parallel int, done func()) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, id := range ids {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.checkOne(ctx, id, level)
			if done != nil {
				done()
			}
		}()
	}
	wg.Wait()
}

// checkOne — проверка одного источника и запись результата. Не больше lightParallel / fullParallel
// одновременно на весь модуль; паника проверки — в журнал, а не падение процесса.
func (m *Module) checkOne(ctx context.Context, id int64, level string) {
	sem := m.lightSem
	if level == "full" {
		sem = m.fullSem
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-sem }()
	defer func() {
		if v := recover(); v != nil {
			m.log.Error("iptv: проверка источника упала", "stream", id, "panic", v)
		}
	}()
	m.mu.Lock()
	s := m.pool.streams[id]
	if s == nil {
		m.mu.Unlock()
		return
	}
	t := probe.Target{URL: s.URL, Kind: s.Kind}
	for _, e := range s.Entries {
		if e.Headers.UserAgent != "" || e.Headers.Referrer != "" {
			t.Headers = e.Headers
			break
		}
	}
	m.mu.Unlock()
	var r probe.Result
	if level == "full" {
		r = m.prober.Full(ctx, t)
	} else {
		r = m.prober.Light(ctx, t)
	}
	if ctx.Err() != nil {
		return
	}
	now := m.now()
	m.mu.Lock()
	s = m.pool.streams[id]
	if s == nil { // источник удалили, пока шла проверка
		m.mu.Unlock()
		return
	}
	applyResult(s, level, r, now)
	snap := *s
	m.mu.Unlock()
	c := check{At: now, Level: level, Grade: r.Grade, Ratio: r.Ratio, TTFB: int(r.TTFB.Milliseconds()), Mbps: r.Mbps, Error: r.Error}
	if err := m.d.saveCheck(ctx, &snap, c); err != nil && ctx.Err() == nil {
		m.log.Warn("iptv: результат проверки не записался", "err", err)
	}
	m.changed()
}

// applyResult — состояние источника после проверки (спека этапа 8, раздел 5.8).
func applyResult(s *Stream, level string, r probe.Result, now time.Time) {
	if r.Kind != "" {
		s.Kind = r.Kind
	}
	if r.TTFB > 0 {
		s.TTFB = int(r.TTFB.Milliseconds())
	}
	if level == "full" {
		s.FullAt = now
	} else {
		s.LightAt = now
	}
	if r.Grade == probe.GradeBlack {
		s.Fails++
		s.State, s.Error = StateSilent, r.Error
		if s.Fails >= 3 {
			s.State = StateDead
		}
		return
	}
	s.State, s.Fails, s.Error = StateAlive, 0, r.Error
	switch r.Grade {
	case probe.GradeGreen, probe.GradeYellow, probe.GradeRed:
		s.Grade, s.Ratio = r.Grade, r.Ratio
		if r.Mbps > 0 {
			s.Mbps = r.Mbps
		}
	}
	switch {
	case r.Height >= 2160:
		s.Quality = "4K"
	case r.Height >= 1080:
		s.Quality = "FHD"
	case r.Height >= 720:
		s.Quality = "HD"
	case r.Height > 0:
		s.Quality = "SD"
	}
}

// ProbeChannel — «Проверить» у канала: полная проверка всех его источников, в том числе из
// «ограниченных» плейлистов. Идёт в фоне.
func (m *Module) ProbeChannel(key string) bool {
	l := m.Lineup()
	c := l.ByKey[key]
	if c == nil {
		return false
	}
	var ids []int64
	for _, s := range append(append([]*Stream{}, c.Sources...), c.Others...) {
		ids = append(ids, s.ID)
	}
	m.background(func(ctx context.Context) {
		m.check(ctx, ids, "full", fullParallel, nil)
		m.rebuild(ctx)
	})
	return true
}

// ProbePlaylist — «Проверить» у плейлиста: лёгкая всех его источников и полная — тех, что стоят у
// видимых каналов.
func (m *Module) ProbePlaylist(id int64) bool {
	m.mu.Lock()
	_, ok := m.pool.playlists[id]
	var ids []int64
	for _, s := range m.pool.streams {
		for _, e := range s.Entries {
			if e.Playlist == id {
				ids = append(ids, s.ID)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.background(func(ctx context.Context) {
		m.check(ctx, ids, "light", lightParallel, nil)
		m.rebuild(ctx)
		mine := map[int64]bool{}
		for _, id := range ids {
			mine[id] = true
		}
		var full []int64
		for _, c := range m.Lineup().Order {
			if c.Hidden != "" {
				continue
			}
			for _, s := range c.Sources {
				if mine[s.ID] {
					full = append(full, s.ID)
				}
			}
		}
		m.check(ctx, full, "full", fullParallel, nil)
		m.rebuild(ctx)
	})
	return true
}

// background — работа на время жизни модуля (из обработчика запроса).
func (m *Module) background(fn func(ctx context.Context)) {
	m.mu.Lock()
	ctx := m.runCtx
	m.mu.Unlock()
	if ctx == nil {
		return
	}
	// Через сторожа: паника в проверке — сбой модуля, а не всего процесса.
	supervisor.Go(ctx, func(ctx context.Context) error {
		fn(ctx)
		return nil
	})
}
