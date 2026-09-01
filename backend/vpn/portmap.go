package vpn

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Reaching a host behind a home router needs a port opened on it. NAT-PMP is
// the simplest protocol that does this and is widely supported (Apple routers,
// most consumer firmware, and anything running OpenWrt with it enabled). When
// it is unavailable the user forwards the port by hand instead — the UI says so
// rather than leaving them guessing.

const (
	natpmpPort    = 5351
	natpmpTimeout = 900 * time.Millisecond
	// Router-side lifetime of the mapping; we refresh well before it lapses.
	mapLifetime = 3600
)

// likelyGateways guesses the router address from the machine's own addresses.
// Home networks put the router at .1 (occasionally .254) often enough that this
// beats parsing routing tables on three operating systems.
func likelyGateways() []net.IP {
	var out []net.IP
	seen := map[string]bool{}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || !ip4.IsPrivate() {
			continue
		}
		for _, last := range []byte{1, 254} {
			gw := net.IPv4(ip4[0], ip4[1], ip4[2], last)
			if masked := gw.Mask(ipnet.Mask); !masked.Equal(ipnet.IP.Mask(ipnet.Mask)) {
				continue
			}
			if key := gw.String(); !seen[key] {
				seen[key] = true
				out = append(out, gw)
			}
		}
	}
	return out
}

// MapPort asks the router to forward externalPort -> local port over TCP.
// Returns the external port the router actually assigned.
func MapPort(localPort int) (int, error) {
	gateways := likelyGateways()
	if len(gateways) == 0 {
		return 0, fmt.Errorf("no private network interface found")
	}

	var lastErr error
	for _, gw := range gateways {
		port, err := natpmpMap(gw, localPort)
		if err == nil {
			return port, nil
		}
		lastErr = err
	}
	return 0, fmt.Errorf("router did not accept a port mapping: %w", lastErr)
}

func natpmpMap(gateway net.IP, localPort int) (int, error) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: gateway, Port: natpmpPort})
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// Request: version 0, op 2 (map TCP), reserved, internal port,
	// suggested external port, lifetime.
	req := make([]byte, 12)
	req[0] = 0
	req[1] = 2
	binary.BigEndian.PutUint16(req[4:], uint16(localPort))
	binary.BigEndian.PutUint16(req[6:], uint16(localPort))
	binary.BigEndian.PutUint32(req[8:], mapLifetime)

	if _, err := conn.Write(req); err != nil {
		return 0, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(natpmpTimeout))
	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil {
		return 0, err
	}
	if n < 16 {
		return 0, fmt.Errorf("short NAT-PMP response")
	}
	if resp[1] != 130 { // 128 + op 2
		return 0, fmt.Errorf("unexpected NAT-PMP opcode %d", resp[1])
	}
	if code := binary.BigEndian.Uint16(resp[2:]); code != 0 {
		return 0, fmt.Errorf("NAT-PMP result code %d", code)
	}
	return int(binary.BigEndian.Uint16(resp[10:])), nil
}

// PublicIP discovers the address the internet sees, via a STUN binding request.
// Without it the host would have to look its own address up on a website before
// it could invite anyone.
func PublicIP(stunServer string) (string, error) {
	addr, err := net.ResolveUDPAddr("udp4", stunServer)
	if err != nil {
		return "", err
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// Binding request: type 0x0001, zero length, magic cookie, random id.
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001)
	binary.BigEndian.PutUint16(req[2:], 0)
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	txID, err := randomBytes(12)
	if err != nil {
		return "", err
	}
	copy(req[8:], txID)

	if _, err := conn.Write(req); err != nil {
		return "", err
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	if n < 20 {
		return "", fmt.Errorf("short STUN response")
	}

	// Walk the attributes looking for XOR-MAPPED-ADDRESS (0x0020).
	body := buf[20:n]
	for len(body) >= 4 {
		attrType := binary.BigEndian.Uint16(body[0:])
		attrLen := int(binary.BigEndian.Uint16(body[2:]))
		if len(body) < 4+attrLen {
			break
		}
		value := body[4 : 4+attrLen]

		if attrType == 0x0020 && len(value) >= 8 && value[1] == 0x01 {
			ip := make([]byte, 4)
			copy(ip, value[4:8])
			// The address is XORed with the magic cookie.
			cookie := []byte{0x21, 0x12, 0xA4, 0x42}
			for i := range ip {
				ip[i] ^= cookie[i]
			}
			return net.IPv4(ip[0], ip[1], ip[2], ip[3]).String(), nil
		}

		advance := 4 + attrLen
		if pad := attrLen % 4; pad != 0 {
			advance += 4 - pad
		}
		if advance > len(body) {
			break
		}
		body = body[advance:]
	}
	return "", fmt.Errorf("STUN response carried no address")
}
