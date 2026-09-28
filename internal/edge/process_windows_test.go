package edge

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestJobHelperProcess — не тест, а «kinodom» для TestChildrenDieWithKinodom: привязывает
// дочерние процессы к себе, запускает долгий дочерний процесс и ждёт, пока его убьют.
func TestJobHelperProcess(t *testing.T) {
	if os.Getenv("KINODOM_EDGE_JOB_HELPER") != "1" {
		t.Skip("запускается из TestChildrenDieWithKinodom")
	}
	if err := bindChildren(); err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	child := exec.Command("ping", "-n", "120", "127.0.0.1") // ping.exe есть в любой Windows; живёт 2 минуты
	if err := child.Start(); err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	fmt.Println("PID", child.Process.Pid)
	time.Sleep(time.Hour)
}

// Kinodom убили жёстко (сбой службы, Диспетчер задач) — его дочерние процессы (Edge) умирают
// вместе с ним и не держат профиль (ревью этапа 4).
func TestChildrenDieWithKinodom(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=^TestJobHelperProcess$", "-test.v")
	helper.Env = append(os.Environ(), "KINODOM_EDGE_JOB_HELPER=1")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	pid := 0
	for sc := bufio.NewScanner(out); pid == 0 && sc.Scan(); {
		line := sc.Text()
		if strings.HasPrefix(line, "ERR") {
			helper.Process.Kill()
			t.Fatal(line)
		}
		if p, ok := strings.CutPrefix(line, "PID "); ok {
			pid, _ = strconv.Atoi(p)
		}
	}
	if pid == 0 {
		helper.Process.Kill()
		t.Fatal("помощник не сообщил номер дочернего процесса")
	}
	helper.Process.Kill() // как «Снять задачу» в Диспетчере задач
	helper.Wait()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return // процесса уже нет
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(h, 1)
		t.Fatal("дочерний процесс пережил kinodom — Edge остался бы держать профиль")
	}
}
