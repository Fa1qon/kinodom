// Package power — запрет сна ПК во время просмотра (спека, раздел 9): пока открыт хоть один
// поток любого модуля (торренты, медиатека, мультикаст) и ещё 10 минут после последнего, служба
// держит запрос Windows PowerRequestSystemRequired. Выключенный или спящий ПК телевизор не
// разбудит — известное ограничение.
package power

import (
	"log/slog"
	"sync"
	"time"
)

// holdAfter — сколько держать запрет после последнего потока: телевизор мог поставить паузу или
// переключиться на следующую серию.
const holdAfter = 10 * time.Minute

// requester — запрос Windows «не засыпать»; в тестах — подделка.
type requester interface {
	set() error
	clear() error
	close() error
}

// Keeper считает открытые потоки и держит запрет сна. Нулевой *Keeper (nil) ничего не делает.
type Keeper struct {
	mu    sync.Mutex
	n     int
	held  bool
	gen   int // номер последнего отпускания: устаревший таймер не снимает запрет
	timer *time.Timer
	after time.Duration
	req   requester
	log   *slog.Logger
}

// New — хранитель запрета сна. Если Windows не дала создать запрос, работает без него (запись
// в журнале): просмотр важнее.
func New(log *slog.Logger) *Keeper {
	r, err := newRequester("Kinodom: идёт просмотр")
	if err != nil {
		log.Warn("запрет сна во время просмотра недоступен", "err", err)
		r = nopRequester{}
	}
	return newKeeper(r, holdAfter, log)
}

func newKeeper(r requester, after time.Duration, log *slog.Logger) *Keeper {
	return &Keeper{req: r, after: after, log: log}
}

// Acquire — начался поток. Возвращает функцию «поток закончился» (повторный вызов безвреден).
func (k *Keeper) Acquire() (release func()) {
	if k == nil {
		return func() {}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n++
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	if !k.held {
		if err := k.req.set(); err != nil {
			k.log.Warn("Windows не приняла запрет сна", "err", err)
		} else {
			k.held = true
		}
	}
	var once sync.Once
	return func() { once.Do(k.release) }
}

func (k *Keeper) release() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n--
	if k.n > 0 {
		return
	}
	k.gen++
	gen := k.gen
	k.timer = time.AfterFunc(k.after, func() { k.expire(gen) })
}

func (k *Keeper) expire(gen int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if gen != k.gen || k.n > 0 || !k.held {
		return
	}
	k.timer = nil
	if err := k.req.clear(); err != nil {
		k.log.Warn("Windows не сняла запрет сна", "err", err)
		return
	}
	k.held = false
}

// Active — сколько потоков открыто сейчас.
func (k *Keeper) Active() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n
}

// Close снимает запрет и закрывает запрос Windows.
func (k *Keeper) Close() error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	k.gen++
	if k.held {
		k.req.clear()
		k.held = false
	}
	return k.req.close()
}

type nopRequester struct{}

func (nopRequester) set() error   { return nil }
func (nopRequester) clear() error { return nil }
func (nopRequester) close() error { return nil }
