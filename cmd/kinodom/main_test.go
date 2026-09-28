package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("код выхода %d, stderr: %s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "kinodom ") {
		t.Fatalf("неожиданный вывод: %q", out.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCLI(nil, &out, &errOut); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "version") {
		t.Fatalf("в подсказке нет списка команд: %q", errOut.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"fly"}, &out, &errOut); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), `неизвестная команда "fly"`) {
		t.Fatalf("нет сообщения об ошибке: %q", errOut.String())
	}
}
