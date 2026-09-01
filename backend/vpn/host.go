package vpn

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// hostMember is one connected participant, as the host sees it.
type hostMember struct {
	member   Member
	conn     net.Conn
	linkKey  []byte
	lastSeen time.Time
}

// Host coordinates a network. It authenticates joiners, keeps the membership
// list and relays traffic between members — without being able to read that
// traffic, which is sealed under member-to-member keys it has no part in.
type Host struct {
	identity   *Identity
	networkKey []byte
	networkID  string
	netName    string
	self       Member

	mu       sync.RWMutex
	members  map[string]*hostMember
	listener net.Listener

	onMembers func([]Member)
	onRelay   func(from string, payload []byte)

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewHost(identity *Identity, netName, password string, self Member) *Host {
	key := DeriveNetworkKey(netName, password)
	self.IsHost = true
	self.PubKey = identity.PublicHex()
	return &Host{
		identity:   identity,
		networkKey: key,
		networkID:  NetworkID(key),
		netName:    netName,
		self:       self,
		members:    make(map[string]*hostMember),
		stopCh:     make(chan struct{}),
	}
}

func (h *Host) OnMembers(fn func([]Member))     { h.onMembers = fn }
func (h *Host) OnRelay(fn func(string, []byte)) { h.onRelay = fn }

// Start listens on port (0 picks a free one) and returns the chosen port.
func (h *Host) Start(port int) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	h.listener = ln
	actual := ln.Addr().(*net.TCPAddr).Port

	go h.acceptLoop(ln)
	go h.expireLoop()
	return actual, nil
}

func (h *Host) Stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
	if h.listener != nil {
		_ = h.listener.Close()
	}
	h.mu.Lock()
	for _, m := range h.members {
		_ = m.conn.Close()
	}
	h.members = make(map[string]*hostMember)
	h.mu.Unlock()
}

func (h *Host) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(conn)
	}
}

// serve runs the handshake and then the read loop for one joiner.
func (h *Host) serve(conn net.Conn) {
	peerID := ""
	defer func() {
		if peerID != "" {
			h.removeMember(peerID, conn)
		}
		_ = conn.Close()
	}()

	reader := bufio.NewReaderSize(conn, 64*1024)

	_ = conn.SetReadDeadline(time.Now().Add(dialTimeout))
	hello, err := readFrame(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || hello.Type != frameHello {
		return
	}

	// The network id is blinded, so this leaks nothing; it only lets the host
	// reject obviously unrelated networks before doing key agreement work.
	if hello.NetworkID != h.networkID {
		_ = writeFrame(conn, Frame{Type: frameReject, Reason: "unknown network"})
		return
	}

	clientPub, err := hex.DecodeString(hello.PubKey)
	if err != nil || len(clientPub) != 32 {
		return
	}
	clientNonce, err := hex.DecodeString(hello.Nonce)
	if err != nil || len(clientNonce) == 0 {
		return
	}

	hostNonce, err := randomBytes(16)
	if err != nil {
		return
	}
	shared, err := SharedSecret(h.identity.Private, clientPub)
	if err != nil {
		return
	}
	// The password is folded in here. A joiner who does not know it derives a
	// different key and cannot produce a frame this host can open — that is the
	// whole of the authentication, with no password material on the wire.
	key, err := linkKey(shared, h.networkKey, clientNonce, hostNonce, "cloudix-vpn-host")
	if err != nil {
		return
	}

	if err := writeFrame(conn, Frame{
		Type:   frameChallenge,
		PubKey: h.identity.PublicHex(),
		Nonce:  hex.EncodeToString(hostNonce),
	}); err != nil {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(dialTimeout))
	joinFrame, err := readFrame(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || joinFrame.Type != frameSealed {
		return
	}

	join, err := openMsg(key, joinFrame.Body)
	if err != nil || join.Kind != msgJoin || join.PeerID == "" {
		// Wrong password lands here: the frame simply will not open.
		_ = writeFrame(conn, Frame{Type: frameReject, Reason: "authentication failed"})
		return
	}
	if join.PubKey != hello.PubKey {
		return // the sealed identity must match the one used for key agreement
	}

	peerID = join.PeerID
	member := Member{
		PeerID:   join.PeerID,
		Name:     join.Name,
		Username: join.Username,
		PubKey:   join.PubKey,
	}

	h.mu.Lock()
	if old, exists := h.members[peerID]; exists && old.conn != conn {
		_ = old.conn.Close()
	}
	h.members[peerID] = &hostMember{member: member, conn: conn, linkKey: key, lastSeen: time.Now()}
	h.mu.Unlock()

	if err := h.sendMsg(conn, key, Msg{
		Kind:    msgWelcome,
		Network: h.netName,
		Members: h.snapshot(),
	}); err != nil {
		return
	}
	h.broadcastMembers()

	for {
		frame, err := readFrame(reader)
		if err != nil {
			return
		}
		if frame.Type != frameSealed {
			continue
		}
		msg, err := openMsg(key, frame.Body)
		if err != nil {
			continue
		}
		h.touch(peerID)
		h.handle(peerID, key, conn, msg)
	}
}

func (h *Host) handle(from string, key []byte, conn net.Conn, msg Msg) {
	switch msg.Kind {
	case msgPing:
		_ = h.sendMsg(conn, key, Msg{Kind: msgPong})

	case msgLeaveHint:
		h.removeMember(from, conn)
		_ = conn.Close()

	case msgRelay:
		payload, err := base64.StdEncoding.DecodeString(msg.Payload)
		if err != nil {
			return
		}
		// Addressed to the host itself.
		if msg.To == h.self.PeerID || msg.To == "" {
			if h.onRelay != nil {
				h.onRelay(from, payload)
			}
			return
		}
		h.mu.RLock()
		target := h.members[msg.To]
		h.mu.RUnlock()
		if target == nil {
			return
		}
		// Forwarded verbatim: the host has no key for this payload.
		_ = h.sendMsg(target.conn, target.linkKey, Msg{
			Kind:    msgRelay,
			From:    from,
			To:      msg.To,
			Payload: msg.Payload,
		})
	}
}

// SendTo delivers a payload from the host to one member.
func (h *Host) SendTo(peerID string, payload []byte) error {
	h.mu.RLock()
	target := h.members[peerID]
	h.mu.RUnlock()
	if target == nil {
		return fmt.Errorf("member %s not connected", peerID)
	}
	return h.sendMsg(target.conn, target.linkKey, Msg{
		Kind:    msgRelay,
		From:    h.self.PeerID,
		To:      peerID,
		Payload: base64.StdEncoding.EncodeToString(payload),
	})
}

func (h *Host) sendMsg(conn net.Conn, key []byte, msg Msg) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	box, err := Seal(key, raw)
	if err != nil {
		return err
	}
	return writeFrame(conn, Frame{
		Type: frameSealed,
		Body: base64.StdEncoding.EncodeToString(box),
	})
}

func (h *Host) snapshot() []Member {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []Member{h.self}
	for _, m := range h.members {
		out = append(out, m.member)
	}
	return out
}

func (h *Host) broadcastMembers() {
	list := h.snapshot()
	if h.onMembers != nil {
		h.onMembers(list)
	}
	h.mu.RLock()
	targets := make([]*hostMember, 0, len(h.members))
	for _, m := range h.members {
		targets = append(targets, m)
	}
	h.mu.RUnlock()

	for _, m := range targets {
		_ = h.sendMsg(m.conn, m.linkKey, Msg{Kind: msgMembers, Members: list, Network: h.netName})
	}
}

func (h *Host) touch(peerID string) {
	h.mu.Lock()
	if m, ok := h.members[peerID]; ok {
		m.lastSeen = time.Now()
	}
	h.mu.Unlock()
}

func (h *Host) removeMember(peerID string, conn net.Conn) {
	h.mu.Lock()
	m, ok := h.members[peerID]
	if ok && m.conn == conn {
		delete(h.members, peerID)
	} else {
		ok = false
	}
	h.mu.Unlock()
	if ok {
		h.broadcastMembers()
	}
}

// expireLoop drops members that stopped sending keep-alives, so a crashed peer
// does not linger in everyone's list.
func (h *Host) expireLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-memberTTL)
			var dropped []net.Conn
			h.mu.Lock()
			for id, m := range h.members {
				if m.lastSeen.Before(cutoff) {
					dropped = append(dropped, m.conn)
					delete(h.members, id)
				}
			}
			h.mu.Unlock()
			if len(dropped) > 0 {
				for _, c := range dropped {
					_ = c.Close()
				}
				h.broadcastMembers()
			}
		}
	}
}

func openMsg(key []byte, body string) (Msg, error) {
	var msg Msg
	box, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return msg, err
	}
	raw, err := Open(key, box)
	if err != nil {
		return msg, err
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return msg, err
	}
	return msg, nil
}
