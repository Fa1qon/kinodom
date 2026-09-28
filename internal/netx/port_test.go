package netx

import (
	"fmt"
	"net"
	"testing"
)

// Порт должен открываться и по TCP, и по UDP: движок торрентов слушает на одном номере оба.
func TestFreeTCPUDPPortIsBindableForBoth(t *testing.T) {
	for i := 0; i < 20; i++ {
		port, err := FreeTCPUDPPort("127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		pc, err := net.ListenPacket("udp4", addr)
		if err != nil {
			t.Fatalf("UDP %s: %v", addr, err)
		}
		ln, err := net.Listen("tcp4", addr)
		pc.Close()
		if err != nil {
			t.Fatalf("TCP %s: %v", addr, err)
		}
		ln.Close()
	}
}
