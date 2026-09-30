package winsvc

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// scm — диспетчер служб Windows (svc/mgr).
type scm struct{}

func (scm) Install(c ServiceConfig) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.CreateService(c.Name, c.Exe, mgrConfig(c), c.Args...)
	if err != nil {
		return err
	}
	defer s.Close()
	return setRecovery(s, c)
}

func (scm) Update(c ServiceConfig) error {
	return withService(c.Name, func(s *mgr.Service) error {
		cfg := mgrConfig(c)
		cfg.BinaryPathName = commandLine(c.Exe, c.Args)
		if err := s.UpdateConfig(cfg); err != nil {
			return err
		}
		return setRecovery(s, c)
	})
}

func mgrConfig(c ServiceConfig) mgr.Config {
	return mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      c.DisplayName,
		Description:      c.Description,
		ServiceStartName: c.Account, // виртуальная учётная запись: без пароля
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
		DelayedAutoStart: c.DelayedStart,
	}
}

// commandLine — строка запуска, как её собирает mgr.CreateService.
func commandLine(exe string, args []string) string {
	s := syscall.EscapeArg(exe)
	for _, a := range args {
		s += " " + syscall.EscapeArg(a)
	}
	return s
}

// setRecovery — перезапуск через RestartDelay при каждом сбое, в том числе при выходе с ошибкой
// без падения; счётчик сбоев сбрасывается через ResetPeriod.
func setRecovery(s *mgr.Service, c ServiceConfig) error {
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: c.RestartDelay}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, uint32(c.ResetPeriod.Seconds())); err != nil {
		return fmt.Errorf("действия при сбое: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("действия при выходе с ошибкой: %w", err)
	}
	return nil
}

// withService открывает службу; нет службы — ErrNotInstalled.
func withService(name string, fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ErrNotInstalled
	}
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(s)
}

// queryService открывает службу только для чтения состояния: так kinodom check работает и без
// прав администратора (mgr.Connect просит полный доступ к диспетчеру служб).
func queryService(name string, fn func(*mgr.Service) error) error {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	sh, err := windows.OpenService(h, n, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ErrNotInstalled
	}
	if err != nil {
		return err
	}
	s := &mgr.Service{Name: name, Handle: sh}
	defer s.Close()
	return fn(s)
}

func (scm) Exists(name string) (bool, error) {
	err := queryService(name, func(*mgr.Service) error { return nil })
	if errors.Is(err, ErrNotInstalled) {
		return false, nil
	}
	return err == nil, err
}

func (scm) Start(name string) error {
	return withService(name, func(s *mgr.Service) error {
		err := s.Start()
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return nil
		}
		return err
	})
}

func (scm) Stop(name string, wait time.Duration) error {
	return withService(name, func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		if st.State != svc.StopPending {
			if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
				return err
			}
		}
		deadline := time.Now().Add(wait)
		for {
			st, err := s.Query()
			if err != nil {
				return err
			}
			if st.State == svc.Stopped {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("не остановилась за %d с", int(wait.Seconds()))
			}
			time.Sleep(300 * time.Millisecond)
		}
	})
}

func (scm) Delete(name string) error {
	return withService(name, func(s *mgr.Service) error { return s.Delete() })
}

func (scm) State(name string) (string, error) {
	var state string
	err := queryService(name, func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		switch st.State {
		case svc.Running:
			state = StateRunning
		case svc.Stopped:
			state = StateStopped
		case svc.Paused:
			state = StatePaused
		default:
			state = StatePending
		}
		return nil
	})
	if errors.Is(err, ErrNotInstalled) {
		return StateNotFound, nil
	}
	return state, err
}
