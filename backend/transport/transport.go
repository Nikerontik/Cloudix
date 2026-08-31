package transport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"cloudix/backend/models"
)

const (
	// maxLineBytes bounds a single newline-delimited envelope. Media travels
	// inline as a base64 data URL, so this must comfortably exceed
	// maxMediaBytes (frontend limit) inflated by base64 (~33%) plus JSON
	// escaping. Anything larger is dropped without killing the connection.
	maxLineBytes = 96 * 1024 * 1024
	// writeTimeout keeps a stale/half-open socket from blocking a Send (and,
	// through it, a bound call like SendMessage) for the OS TCP timeout.
	writeTimeout = 10 * time.Second
)

type Manager struct {
	mu         sync.Mutex
	conns      map[string]net.Conn
	listener   net.Listener
	port       int
	onEnvelope func(models.WireEnvelope)
	onPeerAddr func(peerID, ip string)
}

func NewManager(onEnvelope func(models.WireEnvelope), onPeerAddr func(peerID, ip string)) *Manager {
	return &Manager{
		conns:      make(map[string]net.Conn),
		onEnvelope: onEnvelope,
		onPeerAddr: onPeerAddr,
	}
}

func (m *Manager) Start() (int, error) {
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		return 0, err
	}
	m.listener = ln
	m.port = ln.Addr().(*net.TCPAddr).Port
	go m.acceptLoop()
	return m.port, nil
}

func (m *Manager) Stop() {
	if m.listener != nil {
		_ = m.listener.Close()
	}
	m.mu.Lock()
	for _, c := range m.conns {
		_ = c.Close()
	}
	m.conns = make(map[string]net.Conn)
	m.mu.Unlock()
}

func (m *Manager) acceptLoop() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		go m.readLoop(conn)
	}
}

func (m *Manager) removeConnByValue(target net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for peerID, conn := range m.conns {
		if conn == target {
			delete(m.conns, peerID)
		}
	}
}

func (m *Manager) readLoop(conn net.Conn) {
	defer func() {
		m.removeConnByValue(conn)
		_ = conn.Close()
	}()

	// bufio.Reader (not Scanner) so that a single oversized frame is skipped
	// instead of tearing down the whole connection the way Scanner does on
	// bufio.ErrTooLong.
	reader := bufio.NewReaderSize(conn, 1024*1024)
	registeredPeerID := ""

	for {
		line, err := readLine(reader, maxLineBytes)
		if err != nil {
			return
		}
		if line == nil {
			// Frame exceeded maxLineBytes and was discarded; keep the
			// connection alive for subsequent messages.
			continue
		}

		var env models.WireEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}

		if env.SenderID != "" && registeredPeerID == "" {
			m.mu.Lock()
			if old, exists := m.conns[env.SenderID]; exists && old != conn {
				_ = old.Close()
			}
			m.conns[env.SenderID] = conn
			m.mu.Unlock()
			registeredPeerID = env.SenderID

			if m.onPeerAddr != nil {
				if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
					m.onPeerAddr(env.SenderID, tcpAddr.IP.String())
				}
			}
		}

		if m.onEnvelope != nil {
			m.onEnvelope(env)
		}
	}
}

// readLine reads one '\n'-terminated frame. It returns (nil, nil) when a frame
// is longer than limit (the frame is drained and skipped), and a non-nil error
// only when the connection is unusable.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	over := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !over && len(buf)+len(chunk) > limit {
			over = true
		}
		if !over {
			buf = append(buf, chunk...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		if over {
			return nil, nil
		}
		return buf, nil
	}
}

func (m *Manager) HasConn(peerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.conns[peerID]
	return ok
}

func (m *Manager) getConn(peer models.Peer) (net.Conn, error) {
	m.mu.Lock()
	conn, ok := m.conns[peer.PeerID]
	m.mu.Unlock()
	if ok {
		return conn, nil
	}

	addr := net.JoinHostPort(peer.IP, strconv.Itoa(peer.Port))

	dialer := net.Dialer{Timeout: 3 * time.Second}
	newConn, err := dialer.Dial("tcp4", addr)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if existing, exists := m.conns[peer.PeerID]; exists {
		m.mu.Unlock()
		_ = newConn.Close()
		return existing, nil
	}
	m.conns[peer.PeerID] = newConn
	m.mu.Unlock()

	go m.readLoop(newConn)
	return newConn, nil
}

func (m *Manager) sendOnConn(conn net.Conn, env models.WireEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func (m *Manager) Send(peer models.Peer, env models.WireEnvelope) error {
	conn, err := m.getConn(peer)
	if err != nil {
		return err
	}

	if err := m.sendOnConn(conn, env); err != nil {
		m.mu.Lock()
		if existing, exists := m.conns[peer.PeerID]; exists && existing == conn {
			delete(m.conns, peer.PeerID)
		}
		m.mu.Unlock()
		_ = conn.Close()

		newConn, dialErr := m.getConn(peer)
		if dialErr != nil {
			return fmt.Errorf("reconnect to %s failed: %w", peer.PeerID, dialErr)
		}
		return m.sendOnConn(newConn, env)
	}

	return nil
}
