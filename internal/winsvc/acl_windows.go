//go:build windows

package winsvc

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Права на файлы (не GENERIC_*: в наследуемых записях проводник и icacls показывают их понятнее).
const (
	rightsRead   = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
	rightsModify = rightsRead | windows.FILE_GENERIC_WRITE | windows.DELETE
	rightsFull   = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF // FILE_ALL_ACCESS
)

// acl — права через GetNamedSecurityInfo / SetNamedSecurityInfo: наследуемые записи Windows сама
// раздаёт вложенным файлам и папкам.
type acl struct{}

func (acl) Grant(path, account string, write bool) error {
	sid, err := sidOf(account)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	mask := windows.ACCESS_MASK(rightsRead)
	if write {
		mask = rightsModify
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(sid, mask)}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func (acl) Restrict(path string, full, read []string) error {
	var entries []windows.EXPLICIT_ACCESS
	for _, set := range []struct {
		accounts []string
		mask     windows.ACCESS_MASK
	}{{full, rightsFull}, {read, rightsRead}} {
		for _, a := range set.accounts {
			sid, err := sidOf(a)
			if err != nil {
				return err
			}
			entries = append(entries, entry(sid, set.mask))
		}
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	owner, err := windows.StringToSid(SIDAdmins)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
}

func entry(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

// sidOf — SID учётной записи: строка «S-1-…» или имя (NT SERVICE\Kinodom).
func sidOf(account string) (*windows.SID, error) {
	if strings.HasPrefix(account, "S-1-") {
		return windows.StringToSid(account)
	}
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return nil, fmt.Errorf("учётная запись %s не найдена: %w", account, err)
	}
	return sid, nil
}
