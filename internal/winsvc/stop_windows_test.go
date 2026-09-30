package winsvc

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// fakeControl — служба для алгоритма остановки: состояния по очереди на каждый Query.
type fakeControl struct {
	states   []svc.State
	i        int
	controls int
	reset    bool
	refuse   int // сколько первых «остановить» отклонить: служба ещё не готова их принять
	log      []string
}

func (f *fakeControl) Query() (svc.Status, error) {
	st := f.states[min(f.i, len(f.states)-1)]
	f.i++
	return svc.Status{State: st}, nil
}

func (f *fakeControl) Control(c svc.Cmd) (svc.Status, error) {
	f.controls++
	f.log = append(f.log, "stop")
	if f.refuse > 0 {
		f.refuse--
		return svc.Status{}, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL
	}
	return svc.Status{}, nil
}

func (f *fakeControl) ResetRecoveryActions() error {
	f.reset = true
	f.log = append(f.log, "reset")
	return nil
}

// Остановка перед заменой файлов (ревью I3): сначала снимается перезапуск при сбое — упавшая служба
// не поднимется посреди копирования; «запускается» — ждать, а не отказывать; не остановилась — ошибка.
func TestStopService(t *testing.T) {
	f := &fakeControl{states: []svc.State{svc.StartPending, svc.StartPending, svc.Running, svc.StopPending, svc.Stopped}}
	if err := stopService(f, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if f.controls != 1 || f.log[0] != "reset" {
		t.Fatalf("действия %v", f.log)
	}
	f = &fakeControl{states: []svc.State{svc.Stopped}}
	if err := stopService(f, time.Second, time.Millisecond); err != nil || f.controls != 0 || !f.reset {
		t.Fatalf("остановленная: %v, действия %v", err, f.log)
	}
	f = &fakeControl{states: []svc.State{svc.Running, svc.Running, svc.StopPending, svc.Stopped}, refuse: 1}
	if err := stopService(f, time.Second, time.Millisecond); err != nil || f.controls != 2 {
		t.Fatalf("не приняла «остановить» сразу: %v, действия %v", err, f.log)
	}
	f = &fakeControl{states: []svc.State{svc.Running, svc.StopPending}}
	if err := stopService(f, 20*time.Millisecond, time.Millisecond); err == nil {
		t.Fatal("зависшая служба «остановилась»")
	}
}
