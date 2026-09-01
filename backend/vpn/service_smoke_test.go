package vpn

import (
	"sync"
	"testing"
	"time"
)

// Exercises the real handshake, membership and end-to-end relay: two members
// join a host and exchange a payload the host must not be able to read.
func TestOverlayEndToEnd(t *testing.T) {
	hostID, _ := NewIdentity()
	aID, _ := NewIdentity()
	bID, _ := NewIdentity()

	const name, pass = "Test Net", "correct horse battery"

	hostSvc := NewService(hostID)
	aSvc := NewService(aID)
	bSvc := NewService(bID)

	if _, err := hostSvc.Create(name, pass, Member{PeerID: "H", Name: "host"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer hostSvc.Leave()

	addr := "127.0.0.1:" + itoa(hostSvc.Status().ListenPort)

	var wg sync.WaitGroup
	wg.Add(1)
	var got []byte
	bSvc.OnEnvelope(func(from string, payload []byte) {
		if from == "A" {
			got = payload
			wg.Done()
		}
	})

	if _, err := aSvc.Join(name, pass, addr, Member{PeerID: "A", Name: "alice"}); err != nil {
		t.Fatalf("join A: %v", err)
	}
	defer aSvc.Leave()
	if _, err := bSvc.Join(name, pass, addr, Member{PeerID: "B", Name: "bob"}); err != nil {
		t.Fatalf("join B: %v", err)
	}
	defer bSvc.Leave()

	// Wait for the roster to propagate to A.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(aSvc.Status().Members) >= 3 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if n := len(aSvc.Status().Members); n < 3 {
		t.Fatalf("expected 3 members, got %d", n)
	}

	if err := aSvc.SendEnvelope("B", []byte("hello bob")); err != nil {
		t.Fatalf("send: %v", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("payload never arrived at B")
	}
	if string(got) != "hello bob" {
		t.Fatalf("got %q", got)
	}
}

// A wrong password must not get in.
func TestWrongPasswordRejected(t *testing.T) {
	hostID, _ := NewIdentity()
	badID, _ := NewIdentity()

	hostSvc := NewService(hostID)
	if _, err := hostSvc.Create("Net", "the-right-password", Member{PeerID: "H"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer hostSvc.Leave()

	addr := "127.0.0.1:" + itoa(hostSvc.Status().ListenPort)
	bad := NewService(badID)
	if _, err := bad.Join("Net", "the-wrong-password", addr, Member{PeerID: "X"}); err == nil {
		bad.Leave()
		t.Fatal("a wrong password was accepted")
	}
}

func TestInviteRoundTrip(t *testing.T) {
	code, err := EncodeInvite(Invite{Name: "My Net", Addr: "203.0.113.7:47991"})
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeInvite(code)
	if err != nil {
		t.Fatalf("decode %q: %v", code, err)
	}
	if back.Name != "My Net" || back.Addr != "203.0.113.7:47991" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
