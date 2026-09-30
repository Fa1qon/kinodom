package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"kinodom/internal/config"
	"kinodom/internal/setup"
	"kinodom/internal/winsvc"
)

// cmdCheck — для починки (спека этапа 11a, раздел 4.5): служба, пульт, правила брандмауэра,
// ссылка kinodom://. Код 0 — всё в порядке. Права администратора не нужны.
func cmdCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "использование: kinodom check")
		return 2
	}
	sys := newSystem()
	ok := true
	bad := func(format string, a ...any) {
		ok = false
		fmt.Fprintf(stdout, format+"\n", a...)
	}

	switch st, err := sys.SCM.State(setup.ServiceName); {
	case err != nil:
		bad("Служба: не удалось узнать состояние: %v", err)
	case st == winsvc.StateRunning:
		fmt.Fprintln(stdout, "Служба: работает")
	default:
		bad("Служба: %s", map[string]string{winsvc.StateStopped: "остановлена", winsvc.StateNotFound: "не установлена",
			winsvc.StatePending: "запускается или останавливается", winsvc.StatePaused: "приостановлена"}[st])
	}

	paths := config.NewPaths(config.DefaultHome())
	boot, err := config.LoadBootstrap(paths.Bootstrap)
	if err != nil {
		boot = config.DefaultBootstrap()
	}
	base := "http://127.0.0.1:" + strconv.Itoa(boot.APIPort)
	if st, err := apiStatus(base); err != nil {
		bad("Пульт: не отвечает на %s", base)
	} else {
		fmt.Fprintf(stdout, "Пульт: отвечает, версия %s — http://localhost:%d\n", st.Version, boot.APIPort)
		for _, p := range st.Problems {
			fmt.Fprintln(stdout, "  •", p.Text)
		}
	}

	for _, name := range []string{setup.RuleAPI, setup.RuleTorrents} {
		switch has, err := sys.Firewall.Exists(name); {
		case err != nil:
			bad("Брандмауэр, «%s»: %v", name, err)
		case !has:
			bad("Брандмауэр: нет правила «%s»", name)
		default:
			fmt.Fprintf(stdout, "Брандмауэр: «%s» — есть\n", name)
		}
	}

	dir, err := programDir()
	if err != nil {
		return fail(stderr, err)
	}
	want := setup.OpenCommand(dir)
	switch got, err := sys.Registry.Protocol(setup.Scheme); {
	case err != nil:
		bad("Ссылки kinodom://: %v", err)
	case got == "":
		bad("Ссылки kinodom://: не настроены")
	case got != want:
		bad("Ссылки kinodom:// открывает другая программа: %s", got)
	default:
		fmt.Fprintln(stdout, "Ссылки kinodom://: на месте")
	}

	if !ok {
		fmt.Fprintln(stdout, "Есть неполадки: kinodom install от имени администратора исправит установку")
		return 1
	}
	fmt.Fprintln(stdout, "Всё в порядке")
	return 0
}

// apiState — то, что check берёт из «Состояния» службы.
type apiState struct {
	Version  string `json:"version"`
	Problems []struct {
		Text string `json:"text"`
	} `json:"problems"`
}

func apiStatus(base string) (apiState, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(base + "/api/v1/status")
	if err != nil {
		return apiState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiState{}, fmt.Errorf("ответ %d", resp.StatusCode)
	}
	var st apiState
	err = json.NewDecoder(resp.Body).Decode(&st)
	return st, err
}
