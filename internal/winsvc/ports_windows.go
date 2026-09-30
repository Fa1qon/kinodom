package winsvc

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER

// mibTCPRowOwnerPID и mibTCP6RowOwnerPID — строки таблицы слушающих сокетов (iphlpapi).
type mibTCPRowOwnerPID struct {
	State, LocalAddr, LocalPort, RemoteAddr, RemotePort, OwningPID uint32
}

type mibTCP6RowOwnerPID struct {
	LocalAddr               [16]byte
	LocalScopeID, LocalPort uint32
	RemoteAddr              [16]byte
	RemoteScopeID           uint32
	RemotePort, State       uint32
	OwningPID               uint32
}

// ports — кто слушает порт TCP: таблица сокетов с номерами процессов и имя exe процесса.
type ports struct{}

func (ports) Owner(port int) (bool, string, error) {
	for _, af := range []uint32{windows.AF_INET, windows.AF_INET6} {
		pid, found, err := listener(af, port)
		if err != nil {
			return false, "", err
		}
		if found {
			return true, processName(pid), nil
		}
	}
	return false, "", nil
}

// listener — процесс, слушающий порт в семействе адресов af.
func listener(af uint32, port int) (uint32, bool, error) {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	if size == 0 {
		return 0, false, nil
	}
	buf := make([]byte, size+1024) // таблица могла вырасти между вызовами
	size = uint32(len(buf))
	r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	if r != 0 {
		return 0, false, fmt.Errorf("таблица сокетов: %w", windows.Errno(r))
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0]))
	rows := unsafe.Pointer(&buf[4])
	for i := range uintptr(n) {
		var localPort, pid uint32
		if af == windows.AF_INET {
			row := (*mibTCPRowOwnerPID)(unsafe.Add(rows, i*unsafe.Sizeof(mibTCPRowOwnerPID{})))
			localPort, pid = row.LocalPort, row.OwningPID
		} else {
			row := (*mibTCP6RowOwnerPID)(unsafe.Add(rows, i*unsafe.Sizeof(mibTCP6RowOwnerPID{})))
			localPort, pid = row.LocalPort, row.OwningPID
		}
		if int(localPort&0xff)<<8|int(localPort>>8&0xff) == port { // порядок байтов сети
			return pid, true, nil
		}
	}
	return 0, false, nil
}

// processName — имя exe процесса; "" — не узнать (чужой процесс без прав администратора).
func processName(pid uint32) string {
	if pid == 4 {
		return "System (служба HTTP Windows)"
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}

// isAdmin — процесс запущен с правами администратора (повышенными).
func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// Real — система этого ПК.
func Real() System {
	return System{SCM: scm{}, ACL: acl{}, Firewall: firewall{}, Registry: classes{root: registry.LOCAL_MACHINE, base: `Software\Classes`},
		Ports: ports{}, IsAdmin: isAdmin}
}
