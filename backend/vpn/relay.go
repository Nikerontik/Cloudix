package vpn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Relay transport.
//
// When neither side can accept an inbound connection — carrier-grade NAT being
// the usual reason — both connect outwards to a relay instead, and the relay
// pipes the two together. Everything above this layer is unchanged: the same
// Host/Client handshake runs inside the piped stream.
//
// The relay is deliberately dumb and untrusted:
//   - It is addressed by the blinded network id, never by name or password.
//   - It performs no authentication of members and holds no network key, so it
//     cannot join, read or tamper with anything: every byte it forwards is
//     already sealed by the layer above.
//   - Its own access token exists only to stop strangers using someone's server
//     as free bandwidth. It is not what protects the conversation.
//
// One TCP connection per logical link keeps this simple: the host holds a
// control connection, and opens a fresh data connection for each joiner the
// relay announces.

const (
	relayHost   = "host"    // host -> relay: I serve this room
	relayJoin   = "join"    // joiner -> relay: connect me to this room
	relayAccept = "accept"  // host -> relay: here is the other end of session sid
	relaySess   = "session" // relay -> host: a joiner is waiting
	relayPing   = "ping"    // host -> relay: the room is still wanted
	relayOK     = "ok"
	relayErr    = "error"

	relayDialTimeout    = 10 * time.Second
	relayHandshakeLimit = 8 * 1024
)

type relayMsg struct {
	Type   string `json:"t"`
	Room   string `json:"room,omitempty"`
	Sid    string `json:"sid,omitempty"`
	Token  string `json:"tok,omitempty"`
	Reason string `json:"err,omitempty"`
}

func writeRelayMsg(conn net.Conn, m relayMsg) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func readRelayMsg(r *bufio.Reader) (relayMsg, error) {
	var m relayMsg
	line, err := readLimitedLine(r, relayHandshakeLimit)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return m, fmt.Errorf("decode relay message: %w", err)
	}
	return m, nil
}

// DialViaRelay opens a link to the host of room, through the relay.
func DialViaRelay(relayAddr, room, token string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: relayDialTimeout}
	conn, err := dialer.Dial("tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to relay %s: %w", relayAddr, err)
	}

	if err := writeRelayMsg(conn, relayMsg{Type: relayJoin, Room: room, Token: token}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("relay join: %w", err)
	}

	reader := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(relayDialTimeout))
	resp, err := readRelayMsg(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("relay did not respond: %w", err)
	}
	if resp.Type != relayOK {
		_ = conn.Close()
		if resp.Reason != "" {
			return nil, fmt.Errorf("relay refused: %s", resp.Reason)
		}
		return nil, fmt.Errorf("relay refused the connection")
	}

	// Anything the reader buffered past the handshake must not be lost.
	return &bufferedConn{Conn: conn, r: reader}, nil
}

// bufferedConn hands back bytes the handshake reader read ahead of time.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// RelayListener presents relay-delivered sessions as a net.Listener, so the
// host accepts them exactly as it accepts direct TCP connections.
type RelayListener struct {
	relayAddr string
	room      string
	token     string

	conns    chan net.Conn
	stopCh   chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	control net.Conn
	err     error
}

// ListenViaRelay registers as the host of room and starts accepting sessions.
func ListenViaRelay(relayAddr, room, token string) (*RelayListener, error) {
	l := &RelayListener{
		relayAddr: relayAddr,
		room:      room,
		token:     token,
		conns:     make(chan net.Conn, 4),
		stopCh:    make(chan struct{}),
	}
	if err := l.connectControl(); err != nil {
		return nil, err
	}
	go l.controlLoop()
	return l, nil
}

func (l *RelayListener) connectControl() error {
	dialer := net.Dialer{Timeout: relayDialTimeout}
	conn, err := dialer.Dial("tcp", l.relayAddr)
	if err != nil {
		return fmt.Errorf("connect to relay %s: %w", l.relayAddr, err)
	}

	if err := writeRelayMsg(conn, relayMsg{Type: relayHost, Room: l.room, Token: l.token}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("relay register: %w", err)
	}

	reader := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(relayDialTimeout))
	resp, err := readRelayMsg(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("relay did not respond: %w", err)
	}
	if resp.Type != relayOK {
		_ = conn.Close()
		if resp.Reason != "" {
			return fmt.Errorf("relay refused: %s", resp.Reason)
		}
		return fmt.Errorf("relay refused to register the network")
	}

	l.mu.Lock()
	l.control = conn
	l.mu.Unlock()

	go l.readControl(conn, reader)
	return nil
}

// readControl turns each session announcement into a data connection.
func (l *RelayListener) readControl(conn net.Conn, reader *bufio.Reader) {
	defer conn.Close()
	for {
		msg, err := readRelayMsg(reader)
		if err != nil {
			l.mu.Lock()
			if l.control == conn {
				l.control = nil
			}
			l.mu.Unlock()
			return
		}
		if msg.Type != relaySess || msg.Sid == "" {
			continue // ok replies to pings land here and are simply ignored
		}
		go l.openSession(msg.Sid)
	}
}

func (l *RelayListener) openSession(sid string) {
	dialer := net.Dialer{Timeout: relayDialTimeout}
	conn, err := dialer.Dial("tcp", l.relayAddr)
	if err != nil {
		return
	}
	if err := writeRelayMsg(conn, relayMsg{
		Type:  relayAccept,
		Room:  l.room,
		Sid:   sid,
		Token: l.token,
	}); err != nil {
		_ = conn.Close()
		return
	}

	reader := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(relayDialTimeout))
	resp, err := readRelayMsg(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || resp.Type != relayOK {
		_ = conn.Close()
		return
	}

	select {
	case l.conns <- &bufferedConn{Conn: conn, r: reader}:
	case <-l.stopCh:
		_ = conn.Close()
	}
}

// controlLoop keeps the registration alive: it pings so the relay knows the
// room is still wanted, and re-registers after a relay restart or a dropped
// connection.
func (l *RelayListener) controlLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.mu.Lock()
			conn := l.control
			l.mu.Unlock()

			if conn == nil {
				_ = l.connectControl()
				continue
			}
			if err := writeRelayMsg(conn, relayMsg{Type: relayPing}); err != nil {
				_ = conn.Close()
				l.mu.Lock()
				if l.control == conn {
					l.control = nil
				}
				l.mu.Unlock()
			}
		}
	}
}

func (l *RelayListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.stopCh:
		return nil, net.ErrClosed
	}
}

func (l *RelayListener) Close() error {
	l.stopOnce.Do(func() { close(l.stopCh) })
	l.mu.Lock()
	if l.control != nil {
		_ = l.control.Close()
		l.control = nil
	}
	l.mu.Unlock()
	return nil
}

func (l *RelayListener) Addr() net.Addr { return relayAddrType(l.relayAddr) }

type relayAddrType string

func (r relayAddrType) Network() string { return "relay" }
func (r relayAddrType) String() string  { return string(r) }
