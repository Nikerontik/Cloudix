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
	announceEvery = 2 * time.Second
	peerTTL       = 8 * time.Second
	staleTTL      = 60 * time.Second // FIX: держим "протухших" пиров дольше для сигналов звонков
)

type announcePacket struct {
	PeerID   string `json:"peerId"`
	Name     string `json:"name"`
	Username string `json:"username"`
	TCPPort  int    `json:"tcpPort"`
}

type Service struct {
	mu         sync.RWMutex
	peers      map[string]*models.Peer // "живые" (в пределах peerTTL)
	stalePeers map[string]*models.Peer // FIX: последняя известная запись, живёт дольше
	tcpPort    int
	getProfile func() *models.Profile
	onChange   func()
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func NewService(tcpPort int, getProfile func() *models.Profile, onChange func()) *Service {
	return &Service{
		peers:      make(map[string]*models.Peer),
		stalePeers: make(map[string]*models.Peer),
		tcpPort:    tcpPort,
		getProfile: getProfile,
		onChange:   onChange,
		stopCh:     make(chan struct{}),
	}
}

func (s *Service) Start() error {
	groupAddr, err := net.ResolveUDPAddr("udp4", multicastAddr)
	if err != nil {
		return err
	}
	go s.listenLoop(groupAddr)
	go s.announceLoop(groupAddr)
	go s.expireLoop()
	return nil
}

func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// FIX: рассылаем прощание несколько раз, но не удаляем stalePeers у других –
// это выполняется на стороне получателя автоматически при получении TCPPort == -1.
func (s *Service) AnnounceGoodbye(profile *models.Profile) {
	if profile == nil {
		return
	}
	groupAddr, err := net.ResolveUDPAddr("udp4", multicastAddr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, groupAddr)
	if err != nil {
		return
	}
	defer conn.Close()

	pkt := announcePacket{PeerID: profile.PeerID, TCPPort: -1}
	data, _ := json.Marshal(pkt)
	_, _ = conn.Write(data)
	time.Sleep(50 * time.Millisecond)
	_, _ = conn.Write(data)
	time.Sleep(50 * time.Millisecond)
	_, _ = conn.Write(data)
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

			var pkt announcePacket
			if err := json.Unmarshal(buf[:n], &pkt); err != nil {
				continue
			}
			if pkt.PeerID == "" {
				continue
			}
			profile := s.getProfile()
			if profile != nil && pkt.PeerID == profile.PeerID {
				continue
			}

			if pkt.TCPPort == -1 {
				// FIX: goodbye убирает из "живых", но оставляет в stalePeers
				// для дальнейшей возможности доставки end/reject сигналов.
				s.mu.Lock()
				_, existed := s.peers[pkt.PeerID]
				delete(s.peers, pkt.PeerID)
				s.mu.Unlock()
				if existed && s.onChange != nil {
					s.onChange()
				}
				continue
			}

			now := time.Now().Unix()
			changed := false
			s.mu.Lock()
			existing, ok := s.peers[pkt.PeerID]
			if ok {
				if existing.Name != pkt.Name || existing.Username != pkt.Username || existing.IP != src.IP.String() || existing.Port != pkt.TCPPort {
					changed = true
				}
				existing.Name = pkt.Name
				existing.Username = pkt.Username
				existing.IP = src.IP.String()
				existing.Port = pkt.TCPPort
				existing.LastSeen = now
			} else {
				newPeer := &models.Peer{
					PeerID:   pkt.PeerID,
					Name:     pkt.Name,
					Username: pkt.Username,
					Bio:      "",
					Avatar:   "",
					IP:       src.IP.String(),
					Port:     pkt.TCPPort,
					LastSeen: now,
				}
				// FIX: если у нас уже была stale-запись, наследуем bio/avatar
				if stale, staleOk := s.stalePeers[pkt.PeerID]; staleOk {
					newPeer.Bio = stale.Bio
					newPeer.Avatar = stale.Avatar
				}
				s.peers[pkt.PeerID] = newPeer
				changed = true
			}

			// FIX: всегда обновляем/создаём stale-копию с текущим временем
			stale := *s.peers[pkt.PeerID]
			s.stalePeers[pkt.PeerID] = &stale

			s.mu.Unlock()
			if changed && s.onChange != nil {
				s.onChange()
			}
		}
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
			TCPPort:  s.tcpPort,
		}
		data, err := json.Marshal(pkt)
		if err != nil {
			return
		}
		_, _ = conn.Write(data)
	}

	// Immediate burst on start so a late-joining client is discovered
	// quickly instead of waiting up to announceEvery for the first tick,
	// and to compensate for occasional UDP packet loss.
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
			// FIX: чистим совсем старые stale-записи, чтобы не расти бесконечно
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

// GetPeer возвращает только "живого" (онлайн) пира.
func (s *Service) GetPeer(peerID string) (models.Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[peerID]
	if !ok {
		return models.Peer{}, false
	}
	return *p, true
}

// GetPeerEvenIfStale FIX: используется для сигналов конца звонка (end/reject),
// чтобы они доходили даже если UDP-анонс временно не пришёл, а TCP соединение
// с пиром всё ещё установлено или переустановится по последнему известному адресу.
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
