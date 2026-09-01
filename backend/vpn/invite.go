package vpn

import (
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Invite carries everything needed to reach a network except the password.
// Keeping the password out of the code is deliberate: an invite that leaks —
// forwarded chat message, screenshot — is then not enough to get in on its own.
type Invite struct {
	Name string `json:"n"`
	Addr string `json:"a"`
}

var inviteEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeInvite renders a short, typo-resistant code. Groups of five characters
// are easier to read out over a call.
func EncodeInvite(inv Invite) (string, error) {
	raw, err := json.Marshal(inv)
	if err != nil {
		return "", err
	}
	s := inviteEnc.EncodeToString(raw)

	var b strings.Builder
	for i := 0; i < len(s); i += 5 {
		if i > 0 {
			b.WriteByte('-')
		}
		end := i + 5
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
	}
	return "CLDX-" + b.String(), nil
}

func DecodeInvite(code string) (Invite, error) {
	var inv Invite

	cleaned := strings.ToUpper(strings.TrimSpace(code))
	cleaned = strings.TrimPrefix(cleaned, "CLDX-")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	if cleaned == "" {
		return inv, fmt.Errorf("invite code is empty")
	}

	raw, err := inviteEnc.DecodeString(cleaned)
	if err != nil {
		return inv, fmt.Errorf("invite code is malformed")
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		return inv, fmt.Errorf("invite code is malformed")
	}
	if inv.Addr == "" || inv.Name == "" {
		return inv, fmt.Errorf("invite code is incomplete")
	}
	return inv, nil
}

// NormalizeAddr accepts "host", "host:port" and IPv6 forms, filling in the
// default port when one is missing.
func NormalizeAddr(addr string, defaultPort int) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("address is required")
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr, nil
	}
	return net.JoinHostPort(addr, strconv.Itoa(defaultPort)), nil
}
