//go:build windows

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
	return openService(name, windows.SERVICE_QUERY_STATUS, fn)
}

// openService открывает службу с правами access — не больше, чем нужно действию.
func openService(name string, access uint32, fn func(*mgr.Service) error) error {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	sh, err := windows.OpenService(h, n, access)
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

// Start открывает службу только с правом запуска: так запускает и значок в трее от имени
// пользователя (AllowUserControl).
func (scm) Start(name string) error {
	return openService(name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS, func(s *mgr.Service) error {
		err := s.Start()
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return nil
		}
		return err
	})
}

// Halt — «Выход» в трее: остановить и дождаться, с правами пользователя. Перезапуск при сбое не
// сработает: служба при остановке выходит с кодом 0. Служба ещё запускается — ждём (хвост Х39).
func (scm) Halt(name string, wait time.Duration) error {
	return openService(name, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS, func(s *mgr.Service) error {
		return haltService(s, wait, 300*time.Millisecond)
	})
}

// userControlSDDL — права на службу: как у Windows по умолчанию, плюс запуск (RP) и остановка (WP)
// интерактивным пользователям (IU) — для значка в трее без окна прав.
const userControlSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

func (scm) AllowUserControl(name string) error {
	sd, err := windows.SecurityDescriptorFromString(userControlSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return withService(name, func(s *mgr.Service) error {
		return windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	})
}

func (scm) Stop(name string, wait time.Duration) error {
	return withService(name, func(s *mgr.Service) error { return stopService(s, wait, 300*time.Millisecond) })
}

// haltControl — что нужно остановке от службы (mgr.Service; в тестах — подделка).
type haltControl interface {
	Query() (svc.Status, error)
	Control(svc.Cmd) (svc.Status, error)
}

// serviceControl — остановке перед заменой файлов нужно ещё снять перезапуск при сбое.
type serviceControl interface {
	haltControl
	ResetRecoveryActions() error
}

// stopService останавливает службу и ждёт остановки. Сначала снимает перезапуск при сбое: служба,
// падающая при запуске, иначе поднялась бы посреди замены файлов (Install вернёт перезапуск, Uninstall
// удаляет службу). Пока служба запускается, «остановить» она не принимает — ждём.
func stopService(s serviceControl, wait, step time.Duration) error {
	if err := s.ResetRecoveryActions(); err != nil {
		return fmt.Errorf("перезапуск при сбое не снялся: %w", err)
	}
	return haltService(s, wait, step)
}

// haltService — остановить и дождаться: пока служба запускается, «остановить» она не принимает — ждём.
func haltService(s haltControl, wait, step time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		switch st.State {
		case svc.Stopped:
			return nil
		case svc.Running, svc.Paused:
			_, err := s.Control(svc.Stop)
			if err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("не остановилась за %d с", int(wait.Seconds()))
		}
		time.Sleep(step)
	}
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
