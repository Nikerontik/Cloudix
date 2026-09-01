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

// Client is a member's connection to a network host.
type Client struct {
	identity   *Identity
	networkKey []byte
	networkID  string
	self       Member

	mu      sync.RWMutex
	conn    net.Conn
	linkKey []byte
	members []Member
	network string

	onMembers func([]Member)
	onRelay   func(from string, payload []byte)
	onClosed  func(err error)

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewClient(identity *Identity, netName, password string, self Member) *Client {
	key := DeriveNetworkKey(netName, password)
	self.PubKey = identity.PublicHex()
	return &Client{
		identity:   identity,
		networkKey: key,
		networkID:  NetworkID(key),
		self:       self,
		stopCh:     make(chan struct{}),
	}
}

func (c *Client) OnMembers(fn func([]Member))     { c.onMembers = fn }
func (c *Client) OnRelay(fn func(string, []byte)) { c.onRelay = fn }
func (c *Client) OnClosed(fn func(error))         { c.onClosed = fn }

// Connect performs the handshake and starts the read loop. It returns once the
// host has accepted the join, so a wrong password surfaces immediately rather
// than as a silent failure later.
func (c *Client) Connect(addr string) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	return c.handshake(conn)
}

// ConnectViaRelay reaches the host through a relay rather than dialling it
// directly. The handshake above this point is identical, so the relay gains
// nothing by sitting in the middle.
func (c *Client) ConnectViaRelay(relayAddr, token string) error {
	conn, err := DialViaRelay(relayAddr, c.networkID, token)
	if err != nil {
		return err
	}
	return c.handshake(conn)
}

func (c *Client) handshake(conn net.Conn) error {
	reader := bufio.NewReaderSize(conn, 64*1024)

	nonce, err := randomBytes(16)
	if err != nil {
		_ = conn.Close()
		return err
	}

	if err := writeFrame(conn, Frame{
		Type:      frameHello,
		NetworkID: c.networkID,
		PubKey:    c.identity.PublicHex(),
		Nonce:     hex.EncodeToString(nonce),
	}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send hello: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(dialTimeout))
	resp, err := readFrame(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("no response from host: %w", err)
	}
	if resp.Type == frameReject {
		_ = conn.Close()
		return fmt.Errorf("host refused: %s", resp.Reason)
	}
	if resp.Type != frameChallenge {
		_ = conn.Close()
		return fmt.Errorf("unexpected response %q", resp.Type)
	}

	hostPub, err := hex.DecodeString(resp.PubKey)
	if err != nil || len(hostPub) != 32 {
		_ = conn.Close()
		return fmt.Errorf("host sent an invalid key")
	}
	hostNonce, err := hex.DecodeString(resp.Nonce)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("host sent an invalid nonce")
	}

	shared, err := SharedSecret(c.identity.Private, hostPub)
	if err != nil {
		_ = conn.Close()
		return err
	}
	key, err := linkKey(shared, c.networkKey, nonce, hostNonce, "cloudix-vpn-host")
	if err != nil {
		_ = conn.Close()
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.linkKey = key
	c.mu.Unlock()

	if err := c.send(Msg{
		Kind:     msgJoin,
		PeerID:   c.self.PeerID,
		Name:     c.self.Name,
		Username: c.self.Username,
		PubKey:   c.identity.PublicHex(),
	}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send join: %w", err)
	}

	// The welcome both confirms the password was right and gives us the roster.
	_ = conn.SetReadDeadline(time.Now().Add(dialTimeout))
	welcomeFrame, err := readFrame(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("wrong password or host unreachable")
	}
	if welcomeFrame.Type == frameReject {
		_ = conn.Close()
		return fmt.Errorf("wrong password")
	}
	welcome, err := openMsg(key, welcomeFrame.Body)
	if err != nil || welcome.Kind != msgWelcome {
		_ = conn.Close()
		return fmt.Errorf("wrong password")
	}

	c.setMembers(welcome.Members, welcome.Network)

	go c.readLoop(conn, reader, key)
	go c.keepAlive(conn)
	return nil
}

func (c *Client) readLoop(conn net.Conn, reader *bufio.Reader, key []byte) {
	var closeErr error
	defer func() {
		_ = conn.Close()
		if c.onClosed != nil {
			c.onClosed(closeErr)
		}
	}()

	for {
		frame, err := readFrame(reader)
		if err != nil {
			closeErr = err
			return
		}
		if frame.Type != frameSealed {
			continue
		}
		msg, err := openMsg(key, frame.Body)
		if err != nil {
			continue
		}

		switch msg.Kind {
		case msgMembers:
			c.setMembers(msg.Members, msg.Network)
		case msgRelay:
			payload, err := base64.StdEncoding.DecodeString(msg.Payload)
			if err == nil && c.onRelay != nil {
				c.onRelay(msg.From, payload)
			}
		}
	}
}

func (c *Client) keepAlive(conn net.Conn) {
	ticker := time.NewTicker(keepAliveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			if err := c.send(Msg{Kind: msgPing}); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

// SendTo asks the host to forward an already-sealed payload to another member.
func (c *Client) SendTo(peerID string, payload []byte) error {
	return c.send(Msg{
		Kind:    msgRelay,
		To:      peerID,
		From:    c.self.PeerID,
		Payload: base64.StdEncoding.EncodeToString(payload),
	})
}

func (c *Client) send(msg Msg) error {
	c.mu.RLock()
	conn, key := c.conn, c.linkKey
	c.mu.RUnlock()
	if conn == nil || key == nil {
		return fmt.Errorf("not connected")
	}

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

func (c *Client) setMembers(members []Member, network string) {
	c.mu.Lock()
	c.members = members
	if network != "" {
		c.network = network
	}
	c.mu.Unlock()
	if c.onMembers != nil {
		c.onMembers(members)
	}
}

func (c *Client) Members() []Member {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Member, len(c.members))
	copy(out, c.members)
	return out
}

func (c *Client) Close() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	_ = c.send(Msg{Kind: msgLeaveHint})
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
}
