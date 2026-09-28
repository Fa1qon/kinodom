package torrents

import (
	"strings"
	"testing"
	"time"
)

func TestEvalOpenState(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	cases := []struct {
		name    string
		o       openObs
		want    TorrentState
		errPart string
	}{
		{"есть список файлов", openObs{now: at(100), haveInfo: true}, StateReady, ""},
		{"сеть не готова — ждём", openObs{now: at(300)}, StateConnecting, ""},
		{"сеть готова 29 с без пиров", openObs{now: at(29), noPeersSince: at(0)}, StateConnecting, ""},
		{"сеть готова 30 с без пиров", openObs{now: at(30), noPeersSince: at(0)}, StateError, "Нет раздающих"},
		{"пиры 89 с без метаданных", openObs{now: at(89), activePeers: 3, peersSince: at(0)}, StateMetadata, ""},
		{"пиры 90 с без метаданных", openObs{now: at(90), activePeers: 3, peersSince: at(0)}, StateError, "список файлов"},
		{"хранимая раздача не получает ошибку", openObs{now: at(500), noPeersSince: at(0), stored: true}, StateConnecting, ""},
	}
	for _, c := range cases {
		st, errText := evalOpenState(c.o, noPeersAfter, noMetadataAfter)
		if st != c.want || (c.errPart == "") != (errText == "") || !strings.Contains(errText, c.errPart) {
			t.Errorf("%s: получено %s %q, ожидалось %s %q", c.name, st, errText, c.want, c.errPart)
		}
	}
}
