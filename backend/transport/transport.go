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

type Manager struct {
	mu         sync.Mutex
	conns      map[string]net.Conn
	listener   net.Listener
	port       int
	onEnvelope func(models.WireEnvelope)
	onPeerAddr func(peerID, ip string) // NEW
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

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	registeredPeerID := ""

	for scanner.Scan() {
		line := scanner.Bytes()

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
			if env.SenderID != "" && registeredPeerID == "" {
				m.mu.Lock()
				if old, exists := m.conns[env.SenderID]; exists && old != conn {
					_ = old.Close()
				}
				m.conns[env.SenderID] = conn
				m.mu.Unlock()
				registeredPeerID = env.SenderID

				// ВСТАВКА СЮДА:
				if m.onPeerAddr != nil {
					if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
						m.onPeerAddr(env.SenderID, tcpAddr.IP.String())
					}
				}
			}
		}

		if m.onEnvelope != nil {
			m.onEnvelope(env)
		}
	}

	// scanner.Err() проверяется после выхода из цикла Scan() — устраняет
	// предупреждение линтера scannererr. Ошибка здесь не критична для
	// работы (соединение просто закрывается и чистится в defer выше),
	// но полезна для диагностики обрывов связи.
	if err := scanner.Err(); err != nil {
		_ = err // соединение закрывается в defer; ошибку можно залогировать при необходимости
	}
}

// HasConn сообщает, есть ли уже открытое и зарегистрированное TCP-соединение
// с указанным пиром. FIX: используется в SendSignal как fallback, когда
// discovery ещё/уже не знает пира (например, multicast-анонс не доходит
// через VPN в одну сторону), но входящее TCP-соединение от этого пира уже
// установлено и зарегистрировано в readLoop по SenderID из envelope.
// В таком случае можно отвечать через существующий сокет без нового Dial
// по IP/порту из discovery, который может быть недостижим или устаревшим.
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

	// net.JoinHostPort корректно оборачивает IPv6-адреса в квадратные
	// скобки (например "[fe80::1]:4242"), в отличие от ручной склейки
	// через fmt.Sprintf("%s:%d", ...), которая ломается на IPv6.
	addr := net.JoinHostPort(peer.IP, strconv.Itoa(peer.Port))

	// Явный таймаут критичен: без него net.Dial может висеть до нескольких
	// минут, если peer недостижим (сменил сеть, упал, файрвол блокирует
	// порт). Такой вызов, дошедший до синхронного bound-метода (например
	// SendSignal при принятии звонка), без таймаута подвесил бы весь
	// Wails JS-мост на всё время ожидания Dial.
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
	_, err = conn.Write(data)
	return err
}

func (m *Manager) Send(peer models.Peer, env models.WireEnvelope) error {
	conn, err := m.getConn(peer)
	if err != nil {
		return err
	}

	if err := m.sendOnConn(conn, env); err != nil {
		// Соединение могло протухнуть (peer перезапустился, порт сменился и т.п.).
		// Пробуем один раз переподключиться и повторить отправку.
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
