package vpn

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// DefaultPort is the port a host listens on unless told otherwise.
const DefaultPort = 47991

// DefaultRelayPort is what cloudix-relay listens on out of the box.
const DefaultRelayPort = 47992

// Role of this node in the current network.
const (
	RoleNone   = ""
	RoleHost   = "host"
	RoleMember = "member"
)

// Status is the snapshot the UI renders.
type Status struct {
	Active      bool     `json:"active"`
	Role        string   `json:"role"`
	Network     string   `json:"network"`
	Members     []Member `json:"members"`
	Invite      string   `json:"invite"`
	Transport   string   `json:"transport"` // "direct" or "relay"
	RelayAddr   string   `json:"relayAddr"`
	ListenPort  int      `json:"listenPort"`
	PublicAddr  string   `json:"publicAddr"`
	PortMapped  bool     `json:"portMapped"`
	Fingerprint string   `json:"fingerprint"`
	Error       string   `json:"error"`
}

// Service owns whichever of Host/Client is running and presents one API to the
// app layer. Traffic between members is sealed end to end, so a host relaying
// it cannot read what it forwards.
type Service struct {
	identity *Identity

	mu         sync.RWMutex
	role       string
	netName    string
	networkKey []byte
	host       *Host
	client     *Client
	members    []Member
	invite     string
	transport  string
	relayAddr  string
	listenPort int
	publicAddr string
	portMapped bool
	lastError  string

	onStatus   func(Status)
	onEnvelope func(peerID string, payload []byte)
	// Pinned identity keys, trust-on-first-use. A host that later tries to
	// swap a member's key to intercept traffic will not match.
	pinned func(peerID string) (string, bool)
	pin    func(peerID, pubKey string)
}

func NewService(identity *Identity) *Service {
	return &Service{identity: identity, role: RoleNone}
}

func (s *Service) OnStatus(fn func(Status))           { s.onStatus = fn }
func (s *Service) OnEnvelope(fn func(string, []byte)) { s.onEnvelope = fn }
func (s *Service) SetPinStore(get func(string) (string, bool), put func(string, string)) {
	s.pinned = get
	s.pin = put
}

func (s *Service) Fingerprint() string { return Fingerprint(s.identity.Public[:]) }

// Transport selects how members reach the host.
const (
	TransportDirect = "direct"
	TransportRelay  = "relay"
)

// RelayConfig points at a user-supplied relay. It is deliberately not baked
// into the app: everyone runs their own, or borrows a trusted one.
type RelayConfig struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// Create starts hosting a network. With an empty relay config this listens
// locally (direct mode); with one, it registers on that relay instead, which is
// what makes hosting possible from behind carrier-grade NAT.
func (s *Service) Create(netName, password string, self Member, port int, relay RelayConfig) (Status, error) {
	if netName == "" || password == "" {
		return s.Status(), fmt.Errorf("network name and password are required")
	}
	if len(password) < 8 {
		return s.Status(), fmt.Errorf("password must be at least 8 characters")
	}
	s.Leave()

	host := NewHost(s.identity, netName, password, self)
	host.OnMembers(func(list []Member) { s.updateMembers(list) })
	host.OnRelay(func(from string, payload []byte) { s.deliver(from, payload) })

	useRelay := relay.Addr != ""
	actualPort := 0

	if useRelay {
		addr, err := NormalizeAddr(relay.Addr, DefaultRelayPort)
		if err != nil {
			return s.Status(), err
		}
		if err := host.StartViaRelay(addr, relay.Token); err != nil {
			return s.Status(), err
		}
		relay.Addr = addr
	} else {
		if port == 0 {
			port = DefaultPort
		}
		var err error
		if actualPort, err = host.Start(port); err != nil {
			// The preferred port may be taken; fall back to any free one.
			if actualPort, err = host.Start(0); err != nil {
				return s.Status(), fmt.Errorf("could not open a listening port: %w", err)
			}
		}
	}

	s.mu.Lock()
	s.role = RoleHost
	s.netName = netName
	s.networkKey = DeriveNetworkKey(netName, password)
	s.host = host
	s.listenPort = actualPort
	s.members = []Member{host.self}
	s.lastError = ""
	if useRelay {
		s.transport = TransportRelay
		s.relayAddr = relay.Addr
		// Over a relay the invite needs no address of ours: joiners point at
		// the same relay, so only the network name travels.
		if code, err := EncodeInvite(Invite{Name: netName, Addr: "relay:" + relay.Addr}); err == nil {
			s.invite = code
		}
	} else {
		s.transport = TransportDirect
	}
	s.mu.Unlock()

	if !useRelay {
		// Reachability work is slow and best-effort; do not hold up the UI.
		go s.discoverReachability(netName, actualPort)
	}

	status := s.Status()
	s.emit(status)
	return status, nil
}

// discoverReachability asks the router for a port mapping and finds the public
// address, then publishes an invite code built from whichever address works.
func (s *Service) discoverReachability(netName string, port int) {
	mapped := false
	if external, err := MapPort(port); err == nil {
		mapped = true
		if external != port {
			port = external
		}
	}

	public, err := PublicIP("stun.l.google.com:19302")
	if err != nil {
		public = ""
	}

	addr := ""
	if public != "" {
		addr = fmt.Sprintf("%s:%d", public, port)
	}

	invite := ""
	if addr != "" {
		if code, err := EncodeInvite(Invite{Name: netName, Addr: addr}); err == nil {
			invite = code
		}
	}

	s.mu.Lock()
	if s.role != RoleHost {
		s.mu.Unlock()
		return
	}
	s.portMapped = mapped
	s.publicAddr = addr
	s.invite = invite
	s.mu.Unlock()

	s.emit(s.Status())
}

// Join connects to a network someone else is hosting. With a relay config the
// host address is irrelevant — both sides meet at the relay instead.
func (s *Service) Join(netName, password, addr string, self Member, relay RelayConfig) (Status, error) {
	if netName == "" || password == "" {
		return s.Status(), fmt.Errorf("network name and password are required")
	}

	useRelay := relay.Addr != ""
	normalized := ""
	if useRelay {
		var err error
		if normalized, err = NormalizeAddr(relay.Addr, DefaultRelayPort); err != nil {
			return s.Status(), err
		}
	} else {
		var err error
		if normalized, err = NormalizeAddr(addr, DefaultPort); err != nil {
			return s.Status(), err
		}
	}
	s.Leave()

	client := NewClient(s.identity, netName, password, self)
	client.OnMembers(func(list []Member) { s.updateMembers(list) })
	client.OnRelay(func(from string, payload []byte) { s.deliver(from, payload) })
	client.OnClosed(func(err error) {
		s.mu.Lock()
		stale := s.client == client
		if stale {
			s.lastError = "disconnected from the network host"
			// Clearing the roster matters: without it everyone the network
			// contained keeps showing as online after the host dissolves it,
			// and messages queue up against peers that are no longer there.
			s.members = nil
			s.role = RoleNone
			s.netName = ""
			s.networkKey = nil
			s.invite = ""
			s.transport = ""
			s.client = nil
		}
		s.mu.Unlock()
		if stale {
			s.emit(s.Status())
		}
	})

	if useRelay {
		if err := client.ConnectViaRelay(normalized, relay.Token); err != nil {
			return s.Status(), err
		}
	} else {
		if err := client.Connect(normalized); err != nil {
			return s.Status(), err
		}
	}

	s.mu.Lock()
	s.role = RoleMember
	s.netName = netName
	s.networkKey = DeriveNetworkKey(netName, password)
	s.client = client
	s.lastError = ""
	if useRelay {
		s.transport = TransportRelay
		s.relayAddr = normalized
		s.publicAddr = ""
		if code, err := EncodeInvite(Invite{Name: netName, Addr: "relay:" + normalized}); err == nil {
			s.invite = code
		}
	} else {
		s.transport = TransportDirect
		s.publicAddr = normalized
		if code, err := EncodeInvite(Invite{Name: netName, Addr: normalized}); err == nil {
			s.invite = code
		}
	}
	s.mu.Unlock()

	status := s.Status()
	s.emit(status)
	return status, nil
}

// JoinByInvite is Join with the name and address taken from an invite code.
// A code created on a relay carries "relay:<addr>", so the joiner is routed the
// same way the host is without having to know which mode was used. An explicit
// token still has to be supplied, since codes never carry secrets.
func (s *Service) JoinByInvite(code, password, relayToken string, self Member) (Status, error) {
	inv, err := DecodeInvite(code)
	if err != nil {
		return s.Status(), err
	}
	if addr, ok := strings.CutPrefix(inv.Addr, "relay:"); ok {
		return s.Join(inv.Name, password, "", self, RelayConfig{Addr: addr, Token: relayToken})
	}
	return s.Join(inv.Name, password, inv.Addr, self, RelayConfig{})
}

// Dropped reports that the current session ended for a reason other than the
// user leaving — used when the transport underneath dies.
func (s *Service) Dropped(reason string) {
	s.mu.Lock()
	if s.role == RoleNone {
		s.mu.Unlock()
		return
	}
	host, client := s.host, s.client
	s.host, s.client = nil, nil
	s.role = RoleNone
	s.netName = ""
	s.networkKey = nil
	s.members = nil
	s.invite = ""
	s.transport = ""
	s.relayAddr = ""
	s.listenPort = 0
	s.publicAddr = ""
	s.portMapped = false
	s.lastError = reason
	s.mu.Unlock()

	if host != nil {
		host.Stop()
	}
	if client != nil {
		client.Close()
	}
	s.emit(s.Status())
}

func (s *Service) Leave() {
	s.mu.Lock()
	host, client := s.host, s.client
	s.host, s.client = nil, nil
	s.role = RoleNone
	s.netName = ""
	s.networkKey = nil
	s.members = nil
	s.invite = ""
	s.transport = ""
	s.relayAddr = ""
	s.listenPort = 0
	s.publicAddr = ""
	s.portMapped = false
	s.lastError = ""
	s.mu.Unlock()

	if host != nil {
		host.Stop()
	}
	if client != nil {
		client.Close()
	}
	s.emit(s.Status())
}

func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	members := make([]Member, len(s.members))
	copy(members, s.members)
	return Status{
		Active:      s.role != RoleNone,
		Role:        s.role,
		Network:     s.netName,
		Members:     members,
		Invite:      s.invite,
		Transport:   s.transport,
		RelayAddr:   s.relayAddr,
		ListenPort:  s.listenPort,
		PublicAddr:  s.publicAddr,
		PortMapped:  s.portMapped,
		Fingerprint: Fingerprint(s.identity.Public[:]),
		Error:       s.lastError,
	}
}

// Peers lists everyone except this node.
func (s *Service) Peers(selfPeerID string) []Member {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Member, 0, len(s.members))
	for _, m := range s.members {
		if m.PeerID != selfPeerID {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) HasPeer(peerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.members {
		if m.PeerID == peerID {
			return true
		}
	}
	return false
}

// SendEnvelope seals payload for peerID and hands it to the relay. The host,
// if it is not the recipient, forwards a blob it has no key for.
func (s *Service) SendEnvelope(peerID string, payload []byte) error {
	s.mu.RLock()
	role, host, client, networkKey := s.role, s.host, s.client, s.networkKey
	var peerPub string
	for _, m := range s.members {
		if m.PeerID == peerID {
			peerPub = m.PubKey
			break
		}
	}
	s.mu.RUnlock()

	if role == RoleNone {
		return fmt.Errorf("not connected to a network")
	}
	if peerPub == "" {
		return fmt.Errorf("peer %s is not in the network", peerID)
	}

	pub, err := hex.DecodeString(peerPub)
	if err != nil {
		return fmt.Errorf("peer key is malformed")
	}
	key, err := E2EKey(s.identity.Private, pub, networkKey)
	if err != nil {
		return err
	}
	box, err := Seal(key, payload)
	if err != nil {
		return err
	}

	if role == RoleHost {
		return host.SendTo(peerID, box)
	}
	return client.SendTo(peerID, box)
}

// deliver opens an end-to-end sealed payload from another member.
func (s *Service) deliver(from string, box []byte) {
	s.mu.RLock()
	networkKey := s.networkKey
	var peerPub string
	for _, m := range s.members {
		if m.PeerID == from {
			peerPub = m.PubKey
			break
		}
	}
	s.mu.RUnlock()

	if peerPub == "" || networkKey == nil {
		return
	}
	pub, err := hex.DecodeString(peerPub)
	if err != nil {
		return
	}
	key, err := E2EKey(s.identity.Private, pub, networkKey)
	if err != nil {
		return
	}
	payload, err := Open(key, box)
	if err != nil {
		// Either not meant for us or the key does not match — drop it silently
		// rather than surfacing noise from a misbehaving relay.
		return
	}
	if s.onEnvelope != nil {
		s.onEnvelope(from, payload)
	}
}

// updateMembers refreshes the roster and enforces key pinning: a member whose
// identity key changed is dropped, because that is what an interception attempt
// by a malicious host would look like.
func (s *Service) updateMembers(list []Member) {
	kept := make([]Member, 0, len(list))
	var rejected []string

	for _, m := range list {
		if m.PubKey == "" || m.PeerID == "" {
			continue
		}
		if s.pinned != nil {
			if known, ok := s.pinned(m.PeerID); ok && known != m.PubKey {
				rejected = append(rejected, m.PeerID)
				continue
			}
		}
		if s.pin != nil {
			s.pin(m.PeerID, m.PubKey)
		}
		kept = append(kept, m)
	}

	s.mu.Lock()
	s.members = kept
	if len(rejected) > 0 {
		s.lastError = fmt.Sprintf("identity key changed for %d peer(s); they were ignored", len(rejected))
	}
	s.mu.Unlock()

	s.emit(s.Status())
}

func (s *Service) emit(st Status) {
	if s.onStatus != nil {
		s.onStatus(st)
	}
}
