package playback

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Вспомогательный процесс «сервер»: запускает долгий дочерний, берёт его в задание и падает без Wait.
func TestHelperOrphan(t *testing.T) {
	if os.Getenv("KINODOM_JOB_HELPER") != "1" {
		t.Skip("вспомогательный процесс TestJobKillsChildrenWithServer")
	}
	cmd := exec.Command("cmd", "/c", "ping -n 60 127.0.0.1 >NUL")
	hide(cmd)
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	adopt(cmd.Process)
	fmt.Println(cmd.Process.Pid)
	os.Exit(0)
}

// Сервер упал — дочерний процесс из задания завершён (Review Focus 5).
func TestJobKillsChildrenWithServer(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperOrphan$")
	cmd.Env = append(os.Environ(), "KINODOM_JOB_HELPER=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("вспомогательный: %v %s", err, out)
	}
	lines := strings.Fields(string(out))
	pid, err := strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		t.Fatalf("pid: %q", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			return // процесса нет
		}
		ev, _ := windows.WaitForSingleObject(h, 0)
		windows.CloseHandle(h)
		if ev == windows.WAIT_OBJECT_0 {
			return
		}
		if time.Now().After(deadline) {
			exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run() // не оставлять сироту после провала
			t.Fatal("дочерний процесс пережил «сервер»")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
