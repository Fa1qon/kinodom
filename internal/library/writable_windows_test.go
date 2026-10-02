package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Ревью 14В: проверка записи создавала и удаляла пробный файл — менялось время папки, будился диск. Теперь —
// проверка прав без записи: время папки то же.
func TestWritableKeepsFolderTime(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if !(&Library{}).Writable(dir) {
		t.Fatal("своя временная папка — нельзя писать?")
	}
	if fi, _ := os.Stat(dir); !fi.ModTime().Equal(old) {
		t.Fatalf("время папки изменилось: %v", fi.ModTime())
	}
}

// Review Focus 4 (15Б): папка, где текущему пользователю можно только читать, — false; несуществующая — false.
func TestWritableDeniedFolder(t *testing.T) {
	dir := t.TempDir()
	readOnly, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GRGX;;;WD)(A;OICI;GRGX;;;OW)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := readOnly.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		full, _ := windows.SecurityDescriptorFromString("D:(A;OICI;GA;;;WD)")
		fd, _, _ := full.DACL()
		windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, fd, nil)
	})
	if (&Library{}).Writable(dir) {
		t.Fatal("папка только для чтения — можно писать?")
	}
	if (&Library{}).Writable(dir + `\нет такой`) {
		t.Fatal("несуществующая папка — можно писать?")
	}
}

// Ревью 15Б, Critical 1: «Разрешить доступ» (kinodom grant --write) даёт «Изменение» (0x1301BF, winsvc
// rightsModify) — без «удаления вложенного» (FILE_DELETE_CHILD), его даёт только полный доступ. С таким правом
// создавать и удалять файлы можно — проверка записи говорит «можно». Иначе скачанное шло бы в папку загрузок,
// а «Разрешить доступ» не снимал бы «нет права записи» никогда.
func TestWritableWithModifyRights(t *testing.T) {
	dir := t.TempDir()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;0x1301bf;;;" + tu.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		full, _ := windows.SecurityDescriptorFromString("D:(A;OICI;GA;;;WD)")
		fd, _, _ := full.DACL()
		windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, fd, nil)
	})
	if !(&Library{}).Writable(dir) {
		t.Fatal("право «Изменение» — нельзя писать?")
	}
	// И на деле: создать папку раздачи и файл, удалить их.
	sub := filepath.Join(dir, "Раздача")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "a.mkv")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
}
