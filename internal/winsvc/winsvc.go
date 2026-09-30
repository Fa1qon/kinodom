// Package winsvc — системные операции установки Kinodom (спека этапа 11a, раздел 4.8): диспетчер
// служб, права на папки, брандмауэр, реестр, занятость порта. Здесь — интерфейсы; реализация для
// Windows — в файлах *_windows.go (x/sys/windows, netsh без cmd). Команды установки тестируются
// на подделках этих интерфейсов.
package winsvc

import (
	"errors"
	"time"
)

// Учётные записи для прав на папки: SID встроенных групп (имена на русской Windows другие) и
// виртуальная учётная запись службы.
const (
	SIDSystem      = "S-1-5-18"     // SYSTEM
	SIDAdmins      = "S-1-5-32-544" // Administrators / Администраторы
	ServiceAccount = `NT SERVICE\Kinodom`
)

// ErrNotInstalled — службы с таким именем нет.
var ErrNotInstalled = errors.New("служба не установлена")

// ServiceConfig — служба Windows.
type ServiceConfig struct {
	Name, DisplayName, Description string
	Exe                            string   // полный путь к kinodom.exe
	Args                           []string // аргументы запуска: ["service"]
	Account                        string   // учётная запись: NT SERVICE\Kinodom
	DelayedStart                   bool     // автоматически, отложенный запуск
	RestartDelay                   time.Duration
	ResetPeriod                    time.Duration // сброс счётчика сбоев; перезапуск — и при выходе с ошибкой без падения
}

// Состояния службы (State).
const (
	StateRunning  = "running"
	StateStopped  = "stopped"
	StatePending  = "pending" // запускается или останавливается
	StatePaused   = "paused"
	StateNotFound = "not-installed"
)

// SCM — диспетчер служб.
type SCM interface {
	Install(ServiceConfig) error
	Update(ServiceConfig) error // путь, учётная запись, запуск, восстановление
	Exists(name string) (bool, error)
	Start(name string) error
	Stop(name string, wait time.Duration) error // уже остановлена — nil; нет службы — ErrNotInstalled
	Delete(name string) error
	State(name string) (string, error) // нет службы — StateNotFound без ошибки
}

// ACL — права на папки. account — имя учётной записи или SID («S-1-5-18»).
type ACL interface {
	// Grant добавляет права учётной записи на папку с наследованием: чтение или изменение.
	Grant(path, account string, write bool) error
	// Restrict оставляет на папке только полный доступ перечисленных учётных записей, без
	// наследования от родителя.
	Restrict(path string, accounts []string) error
}

// FirewallRule — входящее правило брандмауэра для всех профилей.
type FirewallRule struct {
	Name      string
	Program   string   // полный путь к программе
	Protocols []string // "TCP", "UDP"
	Port      int
	Remote    string // "LocalSubnet" или "Any"
}

// Firewall — правила брандмауэра по имени: Set заменяет правило с тем же именем.
type Firewall interface {
	Set(FirewallRule) error
	Delete(name string) error // правила нет — nil
	Exists(name string) (bool, error)
}

// Registry — ссылки вида scheme:// для всех пользователей ПК (HKLM\Software\Classes).
type Registry interface {
	SetProtocol(scheme, command string) error
	DeleteProtocol(scheme string) error     // нет — nil
	Protocol(scheme string) (string, error) // команда; нет — ""
}

// Ports — кто слушает порт.
type Ports interface {
	Owner(port int) (busy bool, program string, err error) // program — имя exe; "" — не узнать
}

// System — всё системное, что нужно установке.
type System struct {
	SCM      SCM
	ACL      ACL
	Firewall Firewall
	Registry Registry
	Ports    Ports
	IsAdmin  func() bool
}
