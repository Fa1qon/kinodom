package library

import (
	"os"
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
