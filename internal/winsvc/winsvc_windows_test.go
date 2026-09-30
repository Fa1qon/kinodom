package winsvc

import (
	"net"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Тесты этого файла ничего не меняют в системе: только чтение и своя временная папка. Установка
// службы, брандмауэр и реестр для всех — вживую (задача 14 плана этапа 11a), с согласия заказчика.

func TestPortsOwner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	busy, name, err := ports{}.Owner(port)
	if err != nil || !busy || !strings.HasSuffix(strings.ToLower(name), ".exe") {
		t.Fatalf("слушаем %d: занят %v, программа %q, %v", port, busy, name, err)
	}
	ln.Close()
	if busy, _, err := (ports{}).Owner(port); err != nil || busy {
		t.Fatalf("после закрытия: занят %v, %v", busy, err)
	}
}

func TestPortsOwnerIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 нет:", err)
	}
	defer ln.Close()
	if busy, _, err := (ports{}).Owner(ln.Addr().(*net.TCPAddr).Port); err != nil || !busy {
		t.Fatalf("IPv6: занят %v, %v", busy, err)
	}
}

// aces — SID и маски записей DACL папки; protected — наследование от родителя снято.
func aces(t *testing.T, path string) (map[string]windows.ACCESS_MASK, bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]windows.ACCESS_MASK{}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		out[sid.String()] |= ace.Mask
	}
	return out, ctl&windows.SE_DACL_PROTECTED != 0
}

func TestACLGrantAndRestrict(t *testing.T) {
	dir := t.TempDir()
	const users = "S-1-5-32-545"
	if err := (acl{}).Grant(dir, users, false); err != nil {
		t.Fatal(err)
	}
	got, _ := aces(t, dir)
	if got[users]&windows.FILE_READ_DATA == 0 || got[users]&windows.FILE_WRITE_DATA != 0 {
		t.Fatalf("чтение для Users: %x", got[users])
	}
	if err := (acl{}).Grant(dir, users, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = aces(t, dir); got[users]&windows.FILE_WRITE_DATA == 0 || got[users]&windows.DELETE == 0 {
		t.Fatalf("изменение для Users: %x", got[users])
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid.String()
	if !isAdmin() {
		t.Skip("владельца Administrators ставит только администратор")
	}
	if err := (acl{}).Restrict(dir, []string{SIDSystem, me}, []string{users}); err != nil {
		t.Fatal(err)
	}
	got, protected := aces(t, dir)
	if !protected || len(got) != 3 || got[SIDSystem] == 0 || got[me] == 0 || got[users]&windows.FILE_WRITE_DATA != 0 || got[users]&windows.FILE_READ_DATA == 0 {
		t.Fatalf("после ограничения: защищено %v, записи %v", protected, got)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if owner, _, _ := sd.Owner(); owner.String() != SIDAdmins {
		t.Fatalf("владелец %s", owner)
	}
	if _, err := sidOf(`NT SERVICE\KinodomNoSuchService-7f3a`); err == nil {
		t.Log("учётная запись несуществующей службы нашлась — Windows считает SID служб сама")
	}
}

func TestSCMStateWithoutService(t *testing.T) {
	st, err := scm{}.State("KinodomNoSuchService-7f3a")
	if err != nil || st != StateNotFound {
		t.Fatalf("состояние %q, %v", st, err)
	}
	if ok, err := (scm{}).Exists("KinodomNoSuchService-7f3a"); err != nil || ok {
		t.Fatalf("есть %v, %v", ok, err)
	}
}

func TestFirewallExistsReadOnly(t *testing.T) {
	if ok, err := (firewall{}).Exists("Kinodom — нет такого правила 7f3a"); err != nil || ok {
		t.Fatalf("есть %v, %v", ok, err)
	}
}

func TestRuleArgs(t *testing.T) {
	r := FirewallRule{Name: "Kinodom — пульт", Program: `C:\Program Files\Kinodom\kinodom.exe`, Port: 8090, Remote: "LocalSubnet"}
	want := `advfirewall firewall add rule name="Kinodom — пульт" dir=in action=allow protocol=TCP localport=8090 remoteip=LocalSubnet program="C:\Program Files\Kinodom\kinodom.exe" profile=any enable=yes`
	if got := ruleArgs(r, "TCP"); got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
}

func TestRegistryProtocolMissing(t *testing.T) {
	c := classes{root: registry.CURRENT_USER, base: `Software\KinodomNoSuchTest-7f3a`}
	if cmd, err := c.Protocol("kinodom"); err != nil || cmd != "" {
		t.Fatalf("команда %q, %v", cmd, err)
	}
	if err := c.DeleteProtocol("kinodom"); err != nil {
		t.Fatalf("удаление несуществующего: %v", err)
	}
}

func TestIsAdminDoesNotPanic(t *testing.T) {
	t.Log("администратор:", isAdmin())
}
