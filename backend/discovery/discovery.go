package discovery

import (
	"encoding/json"
	"net"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"cloudix/backend/models"
)

const (
	multicastAddr = "239.255.42.99:47990"
	udpPort       = "47990"
	announceEvery = 2 * time.Second
	peerTTL       = 8 * time.Second
	staleTTL      = 60 * time.Second
)

type announcePacket struct {
	PeerID   string `json:"peerId"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"` // FIX: bio теперь едет прямо в анонсе, а не только через отдельный avatar-обмен
	TCPPort  int    `json:"tcpPort"`
}

type Service struct {
	mu         sync.RWMutex
	peers      map[string]*models.Peer
	stalePeers map[string]*models.Peer
	tcpPort    int
	getProfile func() *models.Profile
	onChange   func()
	onNewPeer  func(peerID string) // NEW: авто-синхронизация профиля при первом обнаружении пира
	stopCh     chan struct{}
	stopOnce   sync.Once

	manualTargets   map[string]string
	manualTargetsMu sync.RWMutex
}

func NewService(tcpPort int, getProfile func() *models.Profile, onChange func(), onNewPeer func(peerID string)) *Service {
	return &Service{
		peers:         make(map[string]*models.Peer),
		stalePeers:    make(map[string]*models.Peer),
		tcpPort:       tcpPort,
		getProfile:    getProfile,
		onChange:      onChange,
		onNewPeer:     onNewPeer,
		stopCh:        make(chan struct{}),
		manualTargets: make(map[string]string),
	}
}

func (s *Service) AddManualTarget(addr string) {
	s.manualTargetsMu.Lock()
	s.manualTargets[addr] = addr
	s.manualTargetsMu.Unlock()
}

func (s *Service) RemoveManualTarget(addr string) {
	s.manualTargetsMu.Lock()
	delete(s.manualTargets, addr)
	s.manualTargetsMu.Unlock()
}

func (s *Service) listManualTargets() []string {
	s.manualTargetsMu.RLock()
	defer s.manualTargetsMu.RUnlock()
	out := make([]string, 0, len(s.manualTargets))
	for _, v := range s.manualTargets {
		out = append(out, v)
	}
	return out
}

func (s *Service) Start() error {
	groupAddr, err := net.ResolveUDPAddr("udp4", multicastAddr)
	if err != nil {
		return err
	}
	go s.listenLoop(groupAddr)
	go s.unicastListenLoop()
	go s.announceLoop(groupAddr)
	go s.expireLoop()
	return nil
}

func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *Service) Restart() error {
	s.Stop()
	s.stopOnce = sync.Once{}
	s.stopCh = make(chan struct{})

	s.mu.Lock()
	s.peers = make(map[string]*models.Peer)
	s.mu.Unlock()

	return s.Start()
}

func (s *Service) AnnounceGoodbye(profile *models.Profile) {
	if profile == nil {
		return
	}
	pkt := announcePacket{PeerID: profile.PeerID, TCPPort: -1}
	data, _ := json.Marshal(pkt)

	groupAddr, err := net.ResolveUDPAddr("udp4", multicastAddr)
	if err == nil {
		if conn, derr := net.DialUDP("udp4", nil, groupAddr); derr == nil {
			for i := 0; i < 3; i++ {
				_, _ = conn.Write(data)
				time.Sleep(50 * time.Millisecond)
			}
			_ = conn.Close()
		}
	}

	s.sendUnicastToAll(data)
}

func (s *Service) UpdatePeerProfile(peerID, name, username, bio, avatar string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if p, ok := s.peers[peerID]; ok {
		changed = p.Name != name || p.Username != username || p.Bio != bio || p.Avatar != avatar
		p.Name, p.Username, p.Bio, p.Avatar = name, username, bio, avatar
	}
	if p, ok := s.stalePeers[peerID]; ok {
		p.Name, p.Username, p.Bio, p.Avatar = name, username, bio, avatar
	}
	if changed && s.onChange != nil {
		go s.onChange()
	}
}

func joinExtraInterfaces(pconn *ipv4.PacketConn, groupIP net.IP) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for i := range ifaces {
		iface := ifaces[i]
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		_ = pconn.JoinGroup(&iface, &net.UDPAddr{IP: groupIP})
	}
}

// FIX (главный фикс асимметрии Windows/Mac): при получении ЛЮБОГО валидного
// анонса (multicast или unicast) мы сразу регистрируем IP отправителя как
// manualTarget для unicast-рассылки. Раньше unicast-fallback заполнялся
// только вручную либо после уже установленного TCP-соединения — из-за этого,
// если multicast работал только в одну сторону (частый случай на смешанных
// сетях/VPN-адаптерах), сторона, которая НЕ может достучаться multicast'ом,
// никогда не узнавала, что её всё-таки видят, и не начинала слать unicast
// в ответ. Теперь достаточно, чтобы хотя бы один анонс дошёл в любую сторону.
func (s *Service) processAnnouncePacket(buf []byte, n int, srcIP net.IP) {
	var pkt announcePacket
	if err := json.Unmarshal(buf[:n], &pkt); err != nil {
		return
	}
	if pkt.PeerID == "" {
		return
	}
	profile := s.getProfile()
	if profile != nil && pkt.PeerID == profile.PeerID {
		return
	}

	if pkt.TCPPort == -1 {
		s.mu.Lock()
		_, existed := s.peers[pkt.PeerID]
		delete(s.peers, pkt.PeerID)
		s.mu.Unlock()
		if existed && s.onChange != nil {
			s.onChange()
		}
		return
	}

	s.AddManualTarget(net.JoinHostPort(srcIP.String(), udpPort))

	now := time.Now().Unix()
	changed := false
	isNew := false
	s.mu.Lock()
	existing, ok := s.peers[pkt.PeerID]
	if ok {
		if existing.Name != pkt.Name || existing.Username != pkt.Username || existing.Bio != pkt.Bio || existing.IP != srcIP.String() || existing.Port != pkt.TCPPort {
			changed = true
		}
		existing.Name = pkt.Name
		existing.Username = pkt.Username
		existing.Bio = pkt.Bio
		existing.IP = srcIP.String()
		existing.Port = pkt.TCPPort
		existing.LastSeen = now
	} else {
		isNew = true
		newPeer := &models.Peer{
			PeerID:   pkt.PeerID,
			Name:     pkt.Name,
			Username: pkt.Username,
			Bio:      pkt.Bio,
			IP:       srcIP.String(),
			Port:     pkt.TCPPort,
			LastSeen: now,
		}
		if stale, staleOk := s.stalePeers[pkt.PeerID]; staleOk {
			if newPeer.Avatar == "" {
				newPeer.Avatar = stale.Avatar
			}
		}
		s.peers[pkt.PeerID] = newPeer
		changed = true
	}

	stale := *s.peers[pkt.PeerID]
	s.stalePeers[pkt.PeerID] = &stale
	s.mu.Unlock()

	if changed && s.onChange != nil {
		s.onChange()
	}
	if isNew && s.onNewPeer != nil {
		go s.onNewPeer(pkt.PeerID)
	}
}

func (s *Service) listenLoop(groupAddr *net.UDPAddr) {
	conn, err := net.ListenMulticastUDP("udp4", nil, groupAddr)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(1 << 20)

	pconn := ipv4.NewPacketConn(conn)
	_ = pconn.SetMulticastLoopback(true)
	joinExtraInterfaces(pconn, groupAddr.IP)

	buf := make([]byte, 65536)
	for {
		select {
		case <-s.stopCh:
			return
		default:
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				select {
				case <-s.stopCh:
					return
				default:
					continue
				}
			}
			s.processAnnouncePacket(buf, n, src.IP)
		}
	}
}

// NEW: unicastListenLoop слушает обычные (не multicast) UDP-пакеты на том же
// порту 47990 — это позволяет принимать анонсы, отправленные напрямую через
// VPN-туннель (Radmin, Hamachi и т.п.), где multicast обычно не форвардится.
func (s *Service) unicastListenLoop() {
	addr, err := net.ResolveUDPAddr("udp4", ":"+udpPort)
	if err != nil {
		return
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(1 << 20)

	buf := make([]byte, 65536)
	for {
		select {
		case <-s.stopCh:
			return
		default:
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				select {
				case <-s.stopCh:
					return
				default:
					continue
				}
			}
			s.processAnnouncePacket(buf, n, src.IP)
		}
	}
}

func (s *Service) sendUnicastToAll(data []byte) {
	targets := s.listManualTargets()
	for _, addr := range targets {
		udpAddr, err := net.ResolveUDPAddr("udp4", addr)
		if err != nil {
			continue
		}
		conn, err := net.DialUDP("udp4", nil, udpAddr)
		if err != nil {
			continue
		}
		_, _ = conn.Write(data)
		_ = conn.Close()
	}
}

func (s *Service) announceLoop(groupAddr *net.UDPAddr) {
	conn, err := net.DialUDP("udp4", nil, groupAddr)
	if err != nil {
		return
	}
	defer conn.Close()

	sendAnnounce := func() {
		profile := s.getProfile()
		if profile == nil {
			return
		}
		pkt := announcePacket{
			PeerID:   profile.PeerID,
			Name:     profile.Name,
			Username: profile.Username,
			Bio:      profile.Bio,
			TCPPort:  s.tcpPort,
		}
		data, err := json.Marshal(pkt)
		if err != nil {
			return
		}
		_, _ = conn.Write(data)
		s.sendUnicastToAll(data)
	}

	for i := 0; i < 3; i++ {
		sendAnnounce()
		time.Sleep(150 * time.Millisecond)
	}

	ticker := time.NewTicker(announceEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			sendAnnounce()
		}
	}
}

func (s *Service) expireLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			now := time.Now().Unix()
			changed := false
			s.mu.Lock()
			for id, p := range s.peers {
				if now-p.LastSeen > int64(peerTTL.Seconds()) {
					delete(s.peers, id)
					changed = true
				}
			}
			for id, p := range s.stalePeers {
				if now-p.LastSeen > int64(staleTTL.Seconds()) {
					delete(s.stalePeers, id)
				}
			}
			s.mu.Unlock()
			if changed && s.onChange != nil {
				s.onChange()
			}
		}
	}
}

func (s *Service) ListPeers() []models.Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, *p)
	}
	return out
}

func (s *Service) GetPeer(peerID string) (models.Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[peerID]
	if !ok {
		return models.Peer{}, false
	}
	return *p, true
}

func (s *Service) GetPeerEvenIfStale(peerID string) (models.Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.peers[peerID]; ok {
		return *p, true
	}
	if p, ok := s.stalePeers[peerID]; ok {
		return *p, true
	}
	return models.Peer{}, false
}
