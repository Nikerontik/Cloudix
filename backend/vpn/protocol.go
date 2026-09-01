package vpn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Wire framing is newline-delimited JSON, matching the rest of Cloudix. Only
// the handshake is readable on the wire; every later frame is a sealed blob.
const (
	frameHello     = "hello"     // joiner -> host, opens the handshake
	frameChallenge = "challenge" // host -> joiner, completes key agreement
	frameSealed    = "sealed"    // either direction, everything after the handshake
	frameReject    = "reject"    // host -> joiner, handshake refused

	maxFrameBytes = 8 * 1024 * 1024
	dialTimeout   = 8 * time.Second
	writeTimeout  = 10 * time.Second
	// Members ping this often; the host drops anyone silent for memberTTL.
	keepAliveEvery = 15 * time.Second
	memberTTL      = 45 * time.Second
)

// Frame is the outer envelope. Only Type is always meaningful; the rest depend
// on the stage of the conversation.
type Frame struct {
	Type      string `json:"t"`
	NetworkID string `json:"nid,omitempty"`
	PubKey    string `json:"pk,omitempty"`  // hex X25519 public key
	Nonce     string `json:"n,omitempty"`   // hex handshake nonce
	Body      string `json:"b,omitempty"`   // base64 sealed payload
	Reason    string `json:"err,omitempty"` // human-readable rejection
}

// Sealed payload kinds, carried inside Frame.Body once the link is encrypted.
const (
	msgJoin      = "join"    // joiner -> host: who I am
	msgWelcome   = "welcome" // host -> joiner: your view of the network
	msgMembers   = "members" // host -> everyone: membership changed
	msgRelay     = "relay"   // member -> host -> member: opaque to the host
	msgPing      = "ping"    // member -> host: keep-alive
	msgPong      = "pong"    // host -> member
	msgLeaveHint = "leave"   // member -> host: going away cleanly
)

// Msg is the decrypted link-layer message.
type Msg struct {
	Kind string `json:"k"`

	// Membership
	PeerID   string   `json:"pid,omitempty"`
	Name     string   `json:"nm,omitempty"`
	Username string   `json:"un,omitempty"`
	PubKey   string   `json:"pk,omitempty"`
	Members  []Member `json:"ms,omitempty"`
	Network  string   `json:"net,omitempty"`

	// Relay. Payload is sealed under the member-to-member key, so the host
	// forwards it without being able to read it.
	To      string `json:"to,omitempty"`
	From    string `json:"from,omitempty"`
	Payload string `json:"pl,omitempty"`
}

// Member is one participant as advertised by the host.
type Member struct {
	PeerID   string `json:"peerId"`
	Name     string `json:"name"`
	Username string `json:"username"`
	PubKey   string `json:"pubKey"`
	IsHost   bool   `json:"isHost"`
}

// writeFrame emits one newline-delimited JSON frame under a write deadline, so
// a stalled peer can never block the caller indefinitely.
func writeFrame(conn net.Conn, f Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func readFrame(r *bufio.Reader) (Frame, error) {
	var f Frame
	line, err := readLimitedLine(r, maxFrameBytes)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(line, &f); err != nil {
		return f, fmt.Errorf("decode frame: %w", err)
	}
	return f, nil
}

// readLimitedLine reads one '\n'-terminated line, refusing anything oversized
// rather than letting a peer exhaust memory.
func readLimitedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > limit {
			return nil, fmt.Errorf("frame exceeds %d bytes", limit)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
}
