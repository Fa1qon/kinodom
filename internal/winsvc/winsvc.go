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
	SIDUsers       = "S-1-5-32-545" // Users / Пользователи
	ServiceAccount = `NT SERVICE\Kinodom`
)

var (
	ErrNotInstalled = errors.New("служба не установлена")                   // службы с таким именем нет
	ErrCancelled    = errors.New("в окне прав администратора нажали «Нет»") // Elevate
)

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
	Start(name string) error                    // хватает прав пользователя, если разрешено AllowUserControl
	Stop(name string, wait time.Duration) error // перед заменой файлов: снимает перезапуск при сбое; уже остановлена — nil; нет службы — ErrNotInstalled
	Halt(name string, wait time.Duration) error // «Выход» в трее: только остановить, прав пользователя хватает
	AllowUserControl(name string) error         // интерактивные пользователи могут запускать и останавливать службу
	Delete(name string) error
	State(name string) (string, error) // нет службы — StateNotFound без ошибки
}

// ACL — права на папки. account — имя учётной записи или SID («S-1-5-18»).
type ACL interface {
	// Grant добавляет права учётной записи на папку с наследованием: чтение или изменение.
	Grant(path, account string, write bool) error
	// Restrict оставляет на папке только перечисленные права — полный доступ full и чтение read, без
	// наследования от родителя; владелец — Administrators (иначе создавший папку заранее пользователь
	// сохранил бы право менять её права).
	Restrict(path string, full, read []string) error
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
	// Автозапуск при входе любого пользователя (HKLM\Software\Microsoft\Windows\CurrentVersion\Run).
	SetAutorun(name, command string) error
	DeleteAutorun(name string) error // нет — nil
}

// Procs — процессы программы.
type Procs interface {
	Close(exe string) error // завершить все процессы этого exe (полный путь), кроме своего
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
	Procs    Procs
	IsAdmin  func() bool
}
