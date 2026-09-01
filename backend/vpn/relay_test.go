package vpn

import (
	"net"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Runs the real cloudix-relay binary and puts a whole network through it: host
// and two members meet at the relay, and a payload travels end to end.
func TestRelayEndToEnd(t *testing.T) {
	bin := t.TempDir() + "/cloudix-relay"
	build := exec.Command("go", "build", "-o", bin, "../../cmd/cloudix-relay")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build relay: %v\n%s", err, out)
	}

	const relayPort = 47993
	const token = "test-token"
	relayAddr := "127.0.0.1:" + strconv.Itoa(relayPort)

	srv := exec.Command(bin, "-addr", relayAddr, "-token", token)
	if err := srv.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	defer func() {
		_ = srv.Process.Kill()
		_ = srv.Wait()
	}()

	// Give the listener a moment to come up.
	waitForPort(t, relayAddr)

	hostID, _ := NewIdentity()
	aID, _ := NewIdentity()
	bID, _ := NewIdentity()

	const name, pass = "Relayed Net", "a-good-long-password"
	cfg := RelayConfig{Addr: relayAddr, Token: token}

	hostSvc := NewService(hostID)
	if _, err := hostSvc.Create(name, pass, Member{PeerID: "H", Name: "host"}, 0, cfg); err != nil {
		t.Fatalf("create via relay: %v", err)
	}
	defer hostSvc.Leave()

	if got := hostSvc.Status().Transport; got != TransportRelay {
		t.Fatalf("transport = %q, want relay", got)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var got []byte
	bSvc := NewService(bID)
	bSvc.OnEnvelope(func(from string, payload []byte) {
		if from == "A" {
			got = payload
			wg.Done()
		}
	})

	aSvc := NewService(aID)
	if _, err := aSvc.Join(name, pass, "", Member{PeerID: "A", Name: "alice"}, cfg); err != nil {
		t.Fatalf("join A via relay: %v", err)
	}
	defer aSvc.Leave()
	if _, err := bSvc.Join(name, pass, "", Member{PeerID: "B", Name: "bob"}, cfg); err != nil {
		t.Fatalf("join B via relay: %v", err)
	}
	defer bSvc.Leave()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if len(aSvc.Status().Members) >= 3 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if n := len(aSvc.Status().Members); n < 3 {
		t.Fatalf("expected 3 members through the relay, got %d", n)
	}

	if err := aSvc.SendEnvelope("B", []byte("through the relay")); err != nil {
		t.Fatalf("send: %v", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("payload never arrived through the relay")
	}
	if string(got) != "through the relay" {
		t.Fatalf("got %q", got)
	}
}

// A wrong relay token must be refused before anything else happens.
func TestRelayTokenRequired(t *testing.T) {
	bin := t.TempDir() + "/cloudix-relay"
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/cloudix-relay").CombinedOutput(); err != nil {
		t.Fatalf("build relay: %v\n%s", err, out)
	}

	relayAddr := "127.0.0.1:47994"
	srv := exec.Command(bin, "-addr", relayAddr, "-token", "right-token")
	if err := srv.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	defer func() {
		_ = srv.Process.Kill()
		_ = srv.Wait()
	}()
	waitForPort(t, relayAddr)

	id, _ := NewIdentity()
	svc := NewService(id)
	_, err := svc.Create("Net", "a-good-long-password", Member{PeerID: "H"}, 0,
		RelayConfig{Addr: relayAddr, Token: "wrong-token"})
	if err == nil {
		svc.Leave()
		t.Fatal("relay accepted a wrong token")
	}
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := dialShort(addr)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("relay never started listening on %s", addr)
}

func dialShort(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, time.Second)
}
