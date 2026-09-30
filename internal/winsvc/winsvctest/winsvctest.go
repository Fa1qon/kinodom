// Package winsvctest — подделка системных операций установки для тестов.
package winsvctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"kinodom/internal/winsvc"
)

// Fake — система на подделках: записывает действия по порядку («scm.install Kinodom»,
// «fw.set …»). Поля меняются под Lock/Unlock или до первого вызова.
type Fake struct {
	mu        sync.Mutex
	acts      []string
	Admin     bool
	Services  map[string]winsvc.ServiceConfig
	Running   map[string]bool
	Stuck     bool   // служба не останавливается
	PortOwner string // "" — порт свободен
	Rules     map[string]winsvc.FirewallRule
	Protocols map[string]string
	Autoruns  map[string]string
	UserCtl   map[string]bool // службам разрешено управление интерактивным пользователям
}

// New — администратор, служб, правил и ссылок нет, порт свободен.
func New() *Fake {
	return &Fake{Admin: true, Services: map[string]winsvc.ServiceConfig{}, Running: map[string]bool{},
		Rules: map[string]winsvc.FirewallRule{}, Protocols: map[string]string{}, Autoruns: map[string]string{}, UserCtl: map[string]bool{}}
}

func (f *Fake) Lock()   { f.mu.Lock() }
func (f *Fake) Unlock() { f.mu.Unlock() }

func (f *Fake) act(format string, args ...any) {
	f.acts = append(f.acts, fmt.Sprintf(format, args...))
}

// Actions — действия по порядку.
func (f *Fake) Actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.acts)
}

// Reset забывает действия.
func (f *Fake) Reset() { f.mu.Lock(); f.acts = nil; f.mu.Unlock() }

// System — подделка как winsvc.System.
func (f *Fake) System() winsvc.System {
	return winsvc.System{SCM: fakeSCM{f}, ACL: fakeACL{f}, Firewall: fakeFW{f}, Registry: fakeReg{f}, Ports: fakePorts{f}, Procs: fakeProcs{f},
		IsAdmin: func() bool { return f.Admin }}
}

type fakeSCM struct{ *Fake }

func (s fakeSCM) Install(c winsvc.ServiceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.install %s", c.Name)
	s.Services[c.Name] = c
	return nil
}

func (s fakeSCM) Update(c winsvc.ServiceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.update %s", c.Name)
	s.Services[c.Name] = c
	return nil
}

func (s fakeSCM) Exists(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Services[name]
	return ok, nil
}

func (s fakeSCM) Start(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.start %s", name)
	s.Running[name] = true
	return nil
}

func (s fakeSCM) Stop(name string, wait time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Services[name]; !ok {
		return winsvc.ErrNotInstalled
	}
	s.act("scm.stop %s", name)
	if s.Stuck && s.Running[name] {
		return errors.New("служба не остановилась за 60 с")
	}
	s.Running[name] = false
	return nil
}

func (s fakeSCM) Halt(name string, wait time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Services[name]; !ok {
		return winsvc.ErrNotInstalled
	}
	s.act("scm.halt %s", name)
	if s.Stuck && s.Running[name] {
		return errors.New("служба не остановилась")
	}
	s.Running[name] = false
	return nil
}

func (s fakeSCM) AllowUserControl(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.allow %s", name)
	s.UserCtl[name] = true
	return nil
}

func (s fakeSCM) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.delete %s", name)
	delete(s.Services, name)
	return nil
}

func (s fakeSCM) State(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Services[name]; !ok {
		return winsvc.StateNotFound, nil
	}
	if s.Running[name] {
		return winsvc.StateRunning, nil
	}
	return winsvc.StateStopped, nil
}

type fakeACL struct{ *Fake }

func (a fakeACL) Grant(path, account string, write bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	mode := "read"
	if write {
		mode = "write"
	}
	a.act("acl.grant %s %s %s", path, account, mode)
	return nil
}

func (a fakeACL) Restrict(path string, full, read []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.act("acl.restrict %s full=%s read=%s", path, strings.Join(full, ","), strings.Join(read, ","))
	return nil
}

type fakeFW struct{ *Fake }

func (w fakeFW) Set(r winsvc.FirewallRule) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.act("fw.set %s %s %d %s %s", r.Name, strings.Join(r.Protocols, ","), r.Port, r.Remote, r.Program)
	w.Rules[r.Name] = r
	return nil
}

func (w fakeFW) Delete(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.act("fw.delete %s", name)
	delete(w.Rules, name)
	return nil
}

func (w fakeFW) Exists(name string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.Rules[name]
	return ok, nil
}

type fakeReg struct{ *Fake }

func (r fakeReg) SetProtocol(scheme, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.set %s %s", scheme, command)
	r.Protocols[scheme] = command
	return nil
}

func (r fakeReg) DeleteProtocol(scheme string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.delete %s", scheme)
	delete(r.Protocols, scheme)
	return nil
}

func (r fakeReg) SetAutorun(name, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.autorun %s %s", name, command)
	r.Autoruns[name] = command
	return nil
}

func (r fakeReg) DeleteAutorun(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.noautorun %s", name)
	delete(r.Autoruns, name)
	return nil
}

func (r fakeReg) Protocol(scheme string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Protocols[scheme], nil
}

type fakeProcs struct{ *Fake }

func (p fakeProcs) Close(exe string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.act("procs.close %s", exe)
	return nil
}

type fakePorts struct{ *Fake }

func (p fakePorts) Owner(int) (bool, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.PortOwner != "", p.PortOwner, nil
}
