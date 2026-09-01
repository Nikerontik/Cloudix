// Command cloudix-relay is the optional meeting point for Cloudix networks.
//
// Run it on any machine reachable from the internet — a cheap VPS is plenty —
// when neither participant can accept inbound connections themselves, which is
// what carrier-grade NAT does to most home connections.
//
// It is deliberately as dumb as possible. It pairs two TCP connections that
// name the same room and copies bytes between them. It never learns the network
// name, the password or any key, and every byte it forwards is already
// encrypted end to end by the participants, so a hostile or compromised relay
// can drop traffic but cannot read or forge it.
//
//	go build -o cloudix-relay ./cmd/cloudix-relay
//	./cloudix-relay -addr :47992 -token "some-shared-secret"
//
// The token is optional and guards the server's bandwidth, not the
// conversation: without it anyone who learns the address can use the relay.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	msgHost    = "host"
	msgJoin    = "join"
	msgAccept  = "accept"
	msgSession = "session"
	msgPing    = "ping"
	msgOK      = "ok"
	msgError   = "error"

	handshakeTimeout = 10 * time.Second
	// A host that vanishes without closing cleanly — app killed, laptop lid
	// shut, network dropped — would otherwise keep its room locked until TCP
	// gave up, which can take minutes. Hosts ping; silence past this releases
	// the room so the host can re-register immediately on restart.
	controlIdleTimeout = 45 * time.Second
	sessionTimeout     = 20 * time.Second
	maxHandshake       = 8 * 1024
	maxRoomIDLen       = 128
)

type message struct {
	Type   string `json:"t"`
	Room   string `json:"room,omitempty"`
	Sid    string `json:"sid,omitempty"`
	Token  string `json:"tok,omitempty"`
	Reason string `json:"err,omitempty"`
}

type room struct {
	control net.Conn
	mu      sync.Mutex
}

type pending struct {
	joiner net.Conn
	ready  chan net.Conn
}

type relay struct {
	token    string
	maxRooms int

	mu       sync.Mutex
	rooms    map[string]*room
	sessions map[string]*pending
}

func main() {
	addr := flag.String("addr", ":47992", "address to listen on")
	token := flag.String("token", "", "shared access token clients must present (optional but recommended)")
	maxRooms := flag.Int("max-rooms", 512, "maximum number of concurrently hosted networks")
	flag.Parse()

	r := &relay{
		token:    *token,
		maxRooms: *maxRooms,
		rooms:    make(map[string]*room),
		sessions: make(map[string]*pending),
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	defer ln.Close()

	if *token == "" {
		log.Printf("WARNING: running without -token; anyone who knows this address can use the relay")
	}
	log.Printf("cloudix-relay listening on %s", *addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go r.handle(conn)
	}
}

func (r *relay) handle(conn net.Conn) {
	reader := bufio.NewReaderSize(conn, 4096)

	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	msg, err := readMsg(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}

	// Constant-time compare so the token cannot be probed by timing.
	if r.token != "" && subtle.ConstantTimeCompare([]byte(msg.Token), []byte(r.token)) != 1 {
		_ = writeMsg(conn, message{Type: msgError, Reason: "invalid token"})
		_ = conn.Close()
		return
	}
	if msg.Room == "" || len(msg.Room) > maxRoomIDLen {
		_ = writeMsg(conn, message{Type: msgError, Reason: "invalid room"})
		_ = conn.Close()
		return
	}

	switch msg.Type {
	case msgHost:
		r.serveHost(conn, msg.Room)
	case msgJoin:
		r.serveJoin(conn, msg.Room)
	case msgAccept:
		r.serveAccept(conn, msg.Sid)
	default:
		_ = writeMsg(conn, message{Type: msgError, Reason: "unknown request"})
		_ = conn.Close()
	}
}

// serveHost registers the control connection for a room and holds it open. The
// room id is an opaque hash to the relay; it carries no recoverable meaning.
func (r *relay) serveHost(conn net.Conn, roomID string) {
	r.mu.Lock()
	if _, exists := r.rooms[roomID]; exists {
		r.mu.Unlock()
		// Whoever registered first keeps the room. A squatter gains nothing:
		// joiners still have to pass the password handshake, which the relay
		// is not part of.
		_ = writeMsg(conn, message{Type: msgError, Reason: "room already hosted"})
		_ = conn.Close()
		return
	}
	if len(r.rooms) >= r.maxRooms {
		r.mu.Unlock()
		_ = writeMsg(conn, message{Type: msgError, Reason: "relay is full"})
		_ = conn.Close()
		return
	}
	rm := &room{control: conn}
	r.rooms[roomID] = rm
	r.mu.Unlock()

	if err := writeMsg(conn, message{Type: msgOK}); err != nil {
		r.dropRoom(roomID, rm)
		_ = conn.Close()
		return
	}
	log.Printf("room %s… hosted", short(roomID))

	// Hold the room for as long as the host keeps pinging.
	reader := bufio.NewReaderSize(conn, 1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(controlIdleTimeout))
		msg, err := readMsg(reader)
		if err != nil {
			break
		}
		if msg.Type == msgPing {
			rm.mu.Lock()
			err = writeMsg(conn, message{Type: msgOK})
			rm.mu.Unlock()
			if err != nil {
				break
			}
		}
	}
	r.dropRoom(roomID, rm)
	_ = conn.Close()
	log.Printf("room %s… released", short(roomID))
}

func (r *relay) dropRoom(roomID string, rm *room) {
	r.mu.Lock()
	if cur, ok := r.rooms[roomID]; ok && cur == rm {
		delete(r.rooms, roomID)
	}
	r.mu.Unlock()
}

// serveJoin parks the joiner and asks the host to open the other end.
func (r *relay) serveJoin(conn net.Conn, roomID string) {
	r.mu.Lock()
	rm := r.rooms[roomID]
	r.mu.Unlock()
	if rm == nil {
		_ = writeMsg(conn, message{Type: msgError, Reason: "nobody is hosting this network"})
		_ = conn.Close()
		return
	}

	sid, err := randomID()
	if err != nil {
		_ = conn.Close()
		return
	}

	p := &pending{joiner: conn, ready: make(chan net.Conn, 1)}
	r.mu.Lock()
	r.sessions[sid] = p
	r.mu.Unlock()

	rm.mu.Lock()
	err = writeMsg(rm.control, message{Type: msgSession, Sid: sid})
	rm.mu.Unlock()
	if err != nil {
		r.dropSession(sid)
		_ = writeMsg(conn, message{Type: msgError, Reason: "host is unreachable"})
		_ = conn.Close()
		return
	}

	select {
	case peer := <-p.ready:
		r.dropSession(sid)
		if err := writeMsg(conn, message{Type: msgOK}); err != nil {
			_ = conn.Close()
			_ = peer.Close()
			return
		}
		pipe(conn, peer)
	case <-time.After(sessionTimeout):
		r.dropSession(sid)
		_ = writeMsg(conn, message{Type: msgError, Reason: "host did not answer"})
		_ = conn.Close()
	}
}

// serveAccept is the host's second connection, completing a session.
func (r *relay) serveAccept(conn net.Conn, sid string) {
	if sid == "" {
		_ = writeMsg(conn, message{Type: msgError, Reason: "missing session"})
		_ = conn.Close()
		return
	}
	r.mu.Lock()
	p := r.sessions[sid]
	if p != nil {
		delete(r.sessions, sid)
	}
	r.mu.Unlock()

	if p == nil {
		_ = writeMsg(conn, message{Type: msgError, Reason: "unknown session"})
		_ = conn.Close()
		return
	}
	if err := writeMsg(conn, message{Type: msgOK}); err != nil {
		_ = conn.Close()
		return
	}
	p.ready <- conn
}

func (r *relay) dropSession(sid string) {
	r.mu.Lock()
	delete(r.sessions, sid)
	r.mu.Unlock()
}

// pipe copies in both directions until either side stops, then closes both.
func pipe(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); closeBoth() }()
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); closeBoth() }()
	wg.Wait()
	closeBoth()
}

func readMsg(r *bufio.Reader) (message, error) {
	var m message
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxHandshake {
			return m, fmt.Errorf("handshake too large")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return m, err
		}
		break
	}
	if err := json.Unmarshal(buf, &m); err != nil {
		return m, err
	}
	return m, nil
}

func writeMsg(conn net.Conn, m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	_, err = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// short trims a room id for logging: enough to correlate entries, not enough to
// be useful anywhere else.
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
