package main

import (
	"net"
	"strings"
	"testing"
)

func TestCheckPortFreeReportsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	err = checkPortFree(port)
	if err == nil || !strings.Contains(err.Error(), "Kinodom уже запущен") {
		t.Fatalf("ожидалась понятная ошибка про занятый порт, получено %v", err)
	}
}

func TestCheckPortFreeOnFreePort(t *testing.T) {
	ln, _ := net.Listen("tcp", ":0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := checkPortFree(port); err != nil {
		t.Fatalf("свободный порт: %v", err)
	}
}

func TestRunRejectsArgs(t *testing.T) {
	var out, errOut strings.Builder
	if code := runCLI([]string{"run", "лишнее"}, &out, &errOut); code != 2 {
		t.Fatalf("код %d", code)
	}
}
