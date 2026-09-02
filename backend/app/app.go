package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"cloudix/backend/discovery"
	"cloudix/backend/models"
	"cloudix/backend/storage"
	"cloudix/backend/transport"
	"cloudix/backend/vpn"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// appVersion is surfaced in Settings; bump it with user-visible releases.
const appVersion = "1.0"

type App struct {
	ctx       context.Context
	store     *storage.Store
	discovery *discovery.Service
	transport *transport.Manager

	profileMu sync.RWMutex
	profile   *models.Profile

	netMu     sync.Mutex
	netReady  bool
	netStopCh chan struct{}

	vpnMu   sync.Mutex
	vpnSvc  *vpn.Service
	vpnPeer map[string]vpn.Member // overlay peers by peerID, for routing
}

func NewApp() *App { return &App{} }

func (a *App) getProfile() *models.Profile {
	a.profileMu.RLock()
	defer a.profileMu.RUnlock()
	return a.profile
}

func (a *App) setProfile(p *models.Profile) {
	a.profileMu.Lock()
	a.profile = p
	a.profileMu.Unlock()
}

// logErr keeps the runtime.LogErrorf(a.ctx, ...) nil-context dance in one place
// for code added after the original pass.
func (a *App) logErr(format string, args ...interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.LogErrorf(a.ctx, format, args...)
}

func (a *App) emitEvent(name string, payload interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, name, payload)
}

// mediaPreview builds the chat-list preview for a message. Media previews are
// stored as locale-neutral tokens (models.PreviewImage/Video/File) and
// translated in the frontend — storing a localized string here used to leak
// Russian text into the English UI.
func mediaPreview(text, mediaKind string) string {
	switch mediaKind {
	case "":
		return text
	case "image":
		return models.PreviewImage
	case "video":
		return models.PreviewVideo
	default:
		return models.PreviewFile
	}
}

// normalizeUsername keeps exactly one leading "@". The frontend shows the @ as a
// fixed prefix, but the rule is enforced here too: a username is how people
// address each other, and every path that writes one — onboarding, Settings, the
// profile editor, an imported file — has to agree on its shape.
//
// An empty username stays empty; callers decide whether that is allowed.
func normalizeUsername(raw string) string {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimLeft(clean, "@")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return ""
	}
	return "@" + clean
}

func genPeerID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "P-" + hex.EncodeToString(b)
}

// peerIP returns the best-known IP for a peer: the address of the live TCP
// connection first, then whatever discovery last saw. WebViews obfuscate their
// local ICE host candidates as "<uuid>.local" mDNS names, and macOS WebKit and
// Windows WebView2 fail to resolve each other's names — so the call layer
// rewrites those candidates using this address instead.
func (a *App) peerIP(peerID string) string {
	if a.transport != nil {
		if ip := a.transport.RemoteIP(peerID); ip != "" {
			return ip
		}
	}
	if a.discovery != nil {
		if peer, ok := a.discovery.GetPeerEvenIfStale(peerID); ok {
			return peer.IP
		}
	}
	return ""
}

func (a *App) resolvePeer(peerID string) (models.Peer, bool) {
	if a.discovery == nil {
		return models.Peer{}, false
	}
	if peer, ok := a.discovery.GetPeerEvenIfStale(peerID); ok {
		return peer, true
	}
	if a.transport != nil && a.transport.HasConn(peerID) {
		return models.Peer{PeerID: peerID}, true
	}
	return models.Peer{}, false
}

func (a *App) onNewPeerDiscovered(peerID string) {
	if a.discovery == nil || a.transport == nil {
		return
	}
	myProfile := a.getProfile()
	if myProfile == nil || peerID == "" || peerID == myProfile.PeerID {
		return
	}

	peer, ok := a.discovery.GetPeerEvenIfStale(peerID)
	if !ok {
		return
	}

	payload, err := json.Marshal(models.AvatarRequestPayload{})
	if err != nil {
		if a.ctx != nil {
			runtime.LogErrorf(a.ctx, "marshal AvatarRequestPayload failed: %v", err)
		}
		return
	}

	go func() {
		if err := a.transport.Send(peer, models.WireEnvelope{
			Type:     models.EnvelopeTypeAvatarRequest,
			SenderID: myProfile.PeerID,
			Payload:  payload,
		}); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "auto avatar_request to %s failed: %v", peerID, err)
		}
	}()

	// Peer just (re)appeared — push any messages queued while it was offline.
	go a.flushUndelivered()
}

func (a *App) initNetworking() error {
	a.transport = transport.NewManager(a.handleEnvelope, func(peerID, ip string) {
		if a.discovery != nil {
			a.discovery.AddManualTarget(net.JoinHostPort(ip, discovery.UDPPort))
		}
	})

	port, err := a.transport.Start()
	if err != nil {
		return fmt.Errorf("transport.Start: %w", err)
	}

	a.discovery = discovery.NewService(
		port,
		a.getProfile,
		func() {
			if a.ctx != nil && a.discovery != nil {
				runtime.EventsEmit(a.ctx, "peers:update", a.discovery.ListPeers())
			}
		},
		a.onNewPeerDiscovered,
	)

	if err := a.discovery.Start(); err != nil {
		return fmt.Errorf("discovery.Start: %w", err)
	}

	a.netMu.Lock()
	a.netStopCh = make(chan struct{})
	stop := a.netStopCh
	a.netReady = true
	a.netMu.Unlock()

	go a.deliveryFlushLoop(stop)

	return nil
}

// stopNetworking tears down discovery, transport and the delivery-retry loop.
// Safe to call more than once.
func (a *App) stopNetworking() {
	a.netMu.Lock()
	if a.netStopCh != nil {
		close(a.netStopCh)
		a.netStopCh = nil
	}
	a.netReady = false
	a.netMu.Unlock()

	if a.discovery != nil {
		a.discovery.Stop()
		a.discovery = nil
	}
	if a.transport != nil {
		a.transport.Stop()
		a.transport = nil
	}
}

// NetworkReady reports whether discovery/transport started successfully. The
// frontend uses it so the connection badge isn't stuck on "connected" when
// networking actually failed to come up.
func (a *App) NetworkReady() bool {
	a.netMu.Lock()
	defer a.netMu.Unlock()
	return a.netReady
}

// deliveryFlushLoop periodically retries messages that were composed while the
// peer was offline.
func (a *App) deliveryFlushLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.flushUndelivered()
		}
	}
}

func (a *App) flushUndelivered() {
	store := a.store
	transportMgr := a.transport
	profile := a.getProfile()
	if store == nil || transportMgr == nil || profile == nil {
		return
	}

	pending, err := store.ListAllUndelivered(profile.PeerID)
	if err != nil || len(pending) == 0 {
		return
	}

	for _, m := range pending {
		// Reachable means: live on the LAN, an open TCP connection, or present
		// in the overlay network.
		_, overlay := a.overlayMember(m.ChatID)
		live := false
		if a.discovery != nil {
			_, live = a.discovery.GetPeer(m.ChatID)
		}
		if !live && !overlay && !transportMgr.HasConn(m.ChatID) {
			continue
		}
		payload, err := json.Marshal(models.MessagePayload{
			ID:        m.ID,
			Text:      m.Text,
			MediaKind: m.MediaKind,
			MediaData: m.MediaData,
			Timestamp: m.Timestamp,
		})
		if err != nil {
			continue
		}
		if err := a.deliver(m.ChatID, models.WireEnvelope{
			Type:     models.EnvelopeTypeMessage,
			SenderID: profile.PeerID,
			Payload:  payload,
		}); err != nil {
			continue
		}
		if err := store.MarkMessageDelivered(m.ID); err == nil {
			a.emitEvent("message:delivered", map[string]string{"chatId": m.ChatID, "id": m.ID})
		}
	}
}

func (a *App) OnStartup(ctx context.Context) {
	a.ctx = ctx

	store, err := storage.Open()
	if err != nil {
		runtime.LogErrorf(ctx, "storage.Open failed: %v", err)
		return
	}
	a.store = store

	loaded, err := store.LoadProfile()
	if err != nil {
		runtime.LogErrorf(ctx, "LoadProfile failed: %v", err)
	}
	a.setProfile(loaded)

	if err := a.initNetworking(); err != nil {
		runtime.LogErrorf(ctx, "initNetworking failed: %v", err)
		return
	}

	if err := a.initOverlay(); err != nil {
		runtime.LogErrorf(ctx, "initOverlay failed: %v", err)
	}
}

// initOverlay prepares the internet overlay ("Cloudix network"). The identity
// keypair is generated once and reused, so peers can pin it.
func (a *App) initOverlay() error {
	store := a.store
	if store == nil {
		return fmt.Errorf("store not initialized")
	}

	seed, err := store.LoadVPNIdentity()
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}

	var identity *vpn.Identity
	if len(seed) == 32 {
		identity, err = vpn.IdentityFromSeed(seed)
		if err != nil {
			return fmt.Errorf("restore identity: %w", err)
		}
	} else {
		identity, err = vpn.NewIdentity()
		if err != nil {
			return fmt.Errorf("create identity: %w", err)
		}
		if err := store.SaveVPNIdentity(identity.Private[:]); err != nil {
			return fmt.Errorf("save identity: %w", err)
		}
	}

	svc := vpn.NewService(identity)
	svc.SetPinStore(store.PinnedKey, func(peerID, pub string) {
		if err := store.PinKey(peerID, pub); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "PinKey failed: %v", err)
		}
	})
	svc.OnStatus(func(st vpn.Status) {
		a.emitEvent("vpn:status", st)
		a.syncOverlayPeers(st)
	})
	svc.OnEnvelope(func(from string, payload []byte) {
		var env models.WireEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return
		}
		// The relay cannot forge this: the payload was sealed with a key only
		// the two members can derive, so the sender is who the overlay says.
		env.SenderID = from
		a.handleEnvelope(env)
	})

	a.vpnMu.Lock()
	a.vpnSvc = svc
	a.vpnPeer = make(map[string]vpn.Member)
	a.vpnMu.Unlock()
	return nil
}

// syncOverlayPeers keeps the routing table and the UI peer list in step with
// the network roster.
func (a *App) syncOverlayPeers(st vpn.Status) {
	profile := a.getProfile()
	selfID := ""
	if profile != nil {
		selfID = profile.PeerID
	}

	table := make(map[string]vpn.Member)
	for _, m := range st.Members {
		if m.PeerID != "" && m.PeerID != selfID {
			table[m.PeerID] = m
		}
	}

	a.vpnMu.Lock()
	a.vpnPeer = table
	a.vpnMu.Unlock()

	a.emitEvent("peers:update", a.GetOnlinePeers())
}

// ensureChatMeta guarantees a chat row exists for a peer before anything is
// written against it. Discovery only knows peers on the local network, so over
// the overlay this used to find nothing: the row was never created,
// TouchChatLastMessage updated zero rows, and the conversation stayed invisible
// until the user opened it from the "Online" tab by hand.
func (a *App) ensureChatMeta(peerID string) {
	store := a.store
	if store == nil || peerID == "" {
		return
	}

	if a.discovery != nil {
		if peer, ok := a.discovery.GetPeerEvenIfStale(peerID); ok && peer.Name != "" {
			_ = store.UpsertChatMeta(models.ChatMeta{
				PeerID: peerID, Name: peer.Name, Username: peer.Username,
				Bio: peer.Bio, Avatar: peer.Avatar,
				Background: peer.Background, Pattern: peer.Pattern,
			})
			return
		}
	}
	// The overlay roster carries no decoration — it arrives with the first
	// profile exchange over the link.
	if m, ok := a.overlayMember(peerID); ok {
		_ = store.UpsertChatMeta(models.ChatMeta{
			PeerID: peerID, Name: m.Name, Username: m.Username,
		})
		return
	}
	// Nothing known about them yet — still create the row so the message has
	// somewhere to land; the name arrives with the next profile exchange.
	_ = store.UpsertChatMetaIfMissing(peerID)
}

// overlayMember reports whether a peer is reachable over the overlay.
func (a *App) overlayMember(peerID string) (vpn.Member, bool) {
	a.vpnMu.Lock()
	defer a.vpnMu.Unlock()
	m, ok := a.vpnPeer[peerID]
	return m, ok
}

// sendOverlay seals and sends an envelope through the overlay.
func (a *App) sendOverlay(peerID string, env models.WireEnvelope) error {
	a.vpnMu.Lock()
	svc := a.vpnSvc
	a.vpnMu.Unlock()
	if svc == nil {
		return fmt.Errorf("overlay not initialized")
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return svc.SendEnvelope(peerID, payload)
}

// deliver routes an envelope over the LAN transport when the peer is on the
// local network, and over the overlay otherwise.
func (a *App) deliver(peerID string, env models.WireEnvelope) error {
	if peer, ok := a.resolvePeer(peerID); ok && a.transport != nil {
		if err := a.transport.Send(peer, env); err == nil {
			return nil
		} else if _, overlay := a.overlayMember(peerID); !overlay {
			return err
		}
	}
	if _, ok := a.overlayMember(peerID); ok {
		return a.sendOverlay(peerID, env)
	}
	return fmt.Errorf("peer %s is not reachable", peerID)
}

func (a *App) OnBeforeClose(ctx context.Context) bool {
	profile := a.getProfile()
	if profile != nil && a.discovery != nil {
		a.discovery.AnnounceGoodbye(profile)
	}
	if svc := a.vpnService(); svc != nil {
		svc.Leave()
	}
	a.stopNetworking()
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			runtime.LogErrorf(ctx, "store.Close failed: %v", err)
		}
	}
	return false
}

func (a *App) GetProfile() *models.Profile { return a.getProfile() }

func (a *App) Register(name, username, bio, avatar string) (*models.Profile, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	p := models.Profile{
		PeerID:    genPeerID(),
		Name:      name,
		Username:  normalizeUsername(username),
		Bio:       bio,
		Avatar:    avatar,
		CreatedAt: time.Now().Unix(),
	}
	if err := a.store.SaveProfile(p); err != nil {
		return nil, fmt.Errorf("SaveProfile: %w", err)
	}
	a.setProfile(&p)
	return &p, nil
}

func (a *App) UpdateProfile(p models.Profile) error {
	current := a.getProfile()
	if current == nil {
		return fmt.Errorf("no active profile")
	}
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}

	updated := *current
	updated.Name = p.Name
	// The @ is not the user's to remove: it is what makes a username one.
	// Refusing to blank it out entirely keeps peers able to address you.
	if u := normalizeUsername(p.Username); u != "" {
		updated.Username = u
	}
	updated.Bio = p.Bio
	updated.Avatar = p.Avatar
	updated.Background = p.Background
	updated.Pattern = p.Pattern

	if err := a.store.SaveProfile(updated); err != nil {
		return fmt.Errorf("SaveProfile: %w", err)
	}
	a.setProfile(&updated)

	payload, err := json.Marshal(models.ProfileUpdatePayload{
		Name:       updated.Name,
		Username:   updated.Username,
		Bio:        updated.Bio,
		Avatar:     updated.Avatar,
		Background: updated.Background,
		Pattern:    updated.Pattern,
	})
	if err != nil {
		return fmt.Errorf("marshal ProfileUpdatePayload: %w", err)
	}

	targets := map[string]bool{}
	if a.discovery != nil {
		for _, peer := range a.discovery.ListPeers() {
			targets[peer.PeerID] = true
		}
	}
	a.vpnMu.Lock()
	for peerID := range a.vpnPeer {
		targets[peerID] = true
	}
	a.vpnMu.Unlock()

	for peerID := range targets {
		if err := a.deliver(peerID, models.WireEnvelope{
			Type:     models.EnvelopeTypeProfileUpdate,
			SenderID: updated.PeerID,
			Payload:  payload,
		}); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "profile_update send to %s failed: %v", peerID, err)
		}
	}

	return nil
}

func (a *App) Logout() error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}

	oldStore := a.store
	profile := a.getProfile()

	if profile != nil && a.discovery != nil && a.transport != nil {
		chats, _ := oldStore.ListChats(profile.PeerID)
		for _, c := range chats {
			if peer, ok := a.resolvePeer(c.PeerID); ok {
				_ = a.transport.Send(peer, models.WireEnvelope{
					Type:     models.EnvelopeTypeAccountDeleted,
					SenderID: profile.PeerID,
				})
			}
		}
		a.discovery.AnnounceGoodbye(profile)
	}

	// The overlay carries this profile's peer id, so it cannot outlive it.
	if svc := a.vpnService(); svc != nil {
		svc.Leave()
	}
	a.vpnMu.Lock()
	a.vpnPeer = make(map[string]vpn.Member)
	a.vpnMu.Unlock()

	a.stopNetworking()
	if oldStore != nil {
		if err := oldStore.WipeAll(); err != nil {
			return fmt.Errorf("WipeAll: %w", err)
		}
		_ = oldStore.Close()
	}
	a.store = nil
	a.setProfile(nil)

	store, err := storage.Open()
	if err != nil {
		return fmt.Errorf("storage.Open: %w", err)
	}
	a.store = store

	if err := a.initNetworking(); err != nil {
		return err
	}

	return nil
}

// GetOnlinePeers merges peers found on the local network with members of the
// overlay network, so both look the same to the UI.
func (a *App) GetOnlinePeers() []models.Peer {
	store := a.store
	if store == nil {
		return []models.Peer{}
	}

	out := make([]models.Peer, 0, 8)
	seen := make(map[string]bool)

	if a.discovery != nil {
		for _, p := range a.discovery.ListPeers() {
			if store.IsBlocked(p.PeerID) {
				continue
			}
			seen[p.PeerID] = true
			out = append(out, p)
		}
	}

	profile := a.getProfile()
	a.vpnMu.Lock()
	members := make([]vpn.Member, 0, len(a.vpnPeer))
	for _, m := range a.vpnPeer {
		members = append(members, m)
	}
	a.vpnMu.Unlock()

	for _, m := range members {
		if seen[m.PeerID] || store.IsBlocked(m.PeerID) {
			continue
		}
		if profile != nil && m.PeerID == profile.PeerID {
			continue
		}
		out = append(out, models.Peer{
			PeerID:   m.PeerID,
			Name:     m.Name,
			Username: m.Username,
			LastSeen: time.Now().Unix(),
			ViaVPN:   true,
		})
	}
	return out
}

// ------------------------------------------------------- overlay network ---

func (a *App) vpnService() *vpn.Service {
	a.vpnMu.Lock()
	defer a.vpnMu.Unlock()
	return a.vpnSvc
}

func (a *App) vpnSelf() (vpn.Member, error) {
	profile := a.getProfile()
	if profile == nil {
		return vpn.Member{}, fmt.Errorf("no active profile")
	}
	return vpn.Member{
		PeerID:   profile.PeerID,
		Name:     profile.Name,
		Username: profile.Username,
	}, nil
}

// VPNStatus reports the current overlay state for the UI.
func (a *App) VPNStatus() vpn.Status {
	svc := a.vpnService()
	if svc == nil {
		return vpn.Status{}
	}
	return svc.Status()
}

// VPNCreate starts hosting a network. relayAddr empty means direct hosting on
// this machine; otherwise the network is registered on that relay, which is how
// hosting works from behind carrier-grade NAT.
func (a *App) VPNCreate(name, password, relayAddr, relayToken string) (vpn.Status, error) {
	svc := a.vpnService()
	if svc == nil {
		return vpn.Status{}, fmt.Errorf("overlay not initialized")
	}
	self, err := a.vpnSelf()
	if err != nil {
		return vpn.Status{}, err
	}
	return svc.Create(name, password, self, vpn.DefaultPort, vpn.RelayConfig{
		Addr:  relayAddr,
		Token: relayToken,
	})
}

// VPNJoin connects to a network by name and password, either directly to the
// host address or through a relay when relayAddr is set.
func (a *App) VPNJoin(name, password, addr, relayAddr, relayToken string) (vpn.Status, error) {
	svc := a.vpnService()
	if svc == nil {
		return vpn.Status{}, fmt.Errorf("overlay not initialized")
	}
	self, err := a.vpnSelf()
	if err != nil {
		return vpn.Status{}, err
	}
	return svc.Join(name, password, addr, self, vpn.RelayConfig{
		Addr:  relayAddr,
		Token: relayToken,
	})
}

// VPNJoinByInvite connects using an invite code plus the password. The code
// records which transport the host used, so the joiner follows automatically;
// a relay token still has to be supplied separately.
func (a *App) VPNJoinByInvite(code, password, relayToken string) (vpn.Status, error) {
	svc := a.vpnService()
	if svc == nil {
		return vpn.Status{}, fmt.Errorf("overlay not initialized")
	}
	self, err := a.vpnSelf()
	if err != nil {
		return vpn.Status{}, err
	}
	return svc.JoinByInvite(code, password, relayToken, self)
}

func (a *App) VPNLeave() vpn.Status {
	svc := a.vpnService()
	if svc == nil {
		return vpn.Status{}
	}
	svc.Leave()
	return svc.Status()
}

func (a *App) GetChats() []models.Chat {
	profile := a.getProfile()
	if profile == nil || a.store == nil {
		return []models.Chat{}
	}
	chats, err := a.store.ListChats(profile.PeerID)
	if err != nil {
		if a.ctx != nil {
			runtime.LogErrorf(a.ctx, "ListChats failed: %v", err)
		}
		return []models.Chat{}
	}
	return chats
}

func (a *App) GetMessages(peerID string) []models.Message {
	if a.store == nil {
		return []models.Message{}
	}
	msgs, err := a.store.ListMessages(peerID)
	if err != nil {
		if a.ctx != nil {
			runtime.LogErrorf(a.ctx, "ListMessages failed: %v", err)
		}
		return []models.Message{}
	}
	return msgs
}

func (a *App) SendMessage(peerID, text, mediaKind, mediaData string) (*models.Message, error) {
	profile := a.getProfile()
	if profile == nil {
		return nil, fmt.Errorf("no active profile")
	}
	if a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return nil, fmt.Errorf("peerID is required")
	}

	msg := models.Message{
		ID:        genPeerID(),
		ChatID:    peerID,
		SenderID:  profile.PeerID,
		Text:      text,
		MediaKind: mediaKind,
		MediaData: mediaData,
		Timestamp: time.Now().UnixMilli(),
		Delivered: false,
	}

	if err := a.store.InsertMessage(msg); err != nil {
		return nil, fmt.Errorf("InsertMessage: %w", err)
	}

	preview := mediaPreview(text, mediaKind)
	if err := a.store.TouchChatLastMessage(peerID, preview, msg.Timestamp); err != nil && a.ctx != nil {
		runtime.LogErrorf(a.ctx, "TouchChatLastMessage failed: %v", err)
	}

	// Network send is done off the bound call: transport.Send can block on a
	// dial (up to 3s) or a write to a half-open socket. If the peer is
	// unreachable the message stays delivered=0 and deliveryFlushLoop retries
	// it once the peer is back.
	go a.trySendMessage(msg)

	return &msg, nil
}

func (a *App) trySendMessage(msg models.Message) {
	transportMgr := a.transport
	profile := a.getProfile()
	if transportMgr == nil || profile == nil {
		return
	}

	payload, err := json.Marshal(models.MessagePayload{
		ID:        msg.ID,
		Text:      msg.Text,
		MediaKind: msg.MediaKind,
		MediaData: msg.MediaData,
		Timestamp: msg.Timestamp,
	})
	if err != nil {
		return
	}

	if err := a.deliver(msg.ChatID, models.WireEnvelope{
		Type:     models.EnvelopeTypeMessage,
		SenderID: profile.PeerID,
		Payload:  payload,
	}); err != nil {
		if a.ctx != nil {
			runtime.LogErrorf(a.ctx, "Send message to peer failed: %v", err)
		}
		return
	}

	if a.store != nil {
		_ = a.store.MarkMessageDelivered(msg.ID)
	}
	a.emitEvent("message:delivered", map[string]string{"chatId": msg.ChatID, "id": msg.ID})
}

func (a *App) MarkChatRead(peerID string) error {
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}

	ids, err := a.store.MarkMessagesRead(peerID, profile.PeerID)
	if err != nil {
		return fmt.Errorf("MarkMessagesRead: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	payload, err := json.Marshal(models.ReadReceiptPayload{MessageIDs: ids})
	if err != nil {
		return fmt.Errorf("marshal ReadReceiptPayload: %w", err)
	}
	if err := a.deliver(peerID, models.WireEnvelope{
		Type:     models.EnvelopeTypeReadReceipt,
		SenderID: profile.PeerID,
		Payload:  payload,
	}); err != nil && a.ctx != nil {
		runtime.LogErrorf(a.ctx, "Send read_receipt failed: %v", err)
	}

	return nil
}

func (a *App) DeleteMessage(peerID, id, mode string) error {
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if id == "" {
		return fmt.Errorf("message id is required")
	}

	forBoth := mode == "everyone"
	if err := a.store.SoftDeleteMessage(id, forBoth); err != nil {
		return fmt.Errorf("SoftDeleteMessage: %w", err)
	}

	if forBoth {
		payload, err := json.Marshal(models.DeletePayload{ID: id, Mode: mode})
		if err == nil {
			_ = a.deliver(peerID, models.WireEnvelope{
				Type:     models.EnvelopeTypeDeleteMessage,
				SenderID: profile.PeerID,
				Payload:  payload,
			})
		}
	}

	return nil
}

func (a *App) DeleteChat(peerID string) error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}
	if err := a.store.DeleteChat(peerID); err != nil {
		return fmt.Errorf("DeleteChat: %w", err)
	}
	return nil
}

func (a *App) StartChatWithPeer(peerID, name, username, bio, avatar, background, pattern string) error {
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}

	if err := a.store.UpsertChatMeta(models.ChatMeta{
		PeerID: peerID, Name: name, Username: username, Bio: bio,
		Avatar: avatar, Background: background, Pattern: pattern,
	}); err != nil {
		return fmt.Errorf("UpsertChatMeta: %w", err)
	}

	payload, err := json.Marshal(models.AvatarRequestPayload{})
	if err == nil {
		_ = a.deliver(peerID, models.WireEnvelope{
			Type:     models.EnvelopeTypeAvatarRequest,
			SenderID: profile.PeerID,
			Payload:  payload,
		})
	}

	return nil
}

func (a *App) BlockPeer(peerID string) error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}
	if err := a.store.BlockPeer(peerID); err != nil {
		return fmt.Errorf("BlockPeer: %w", err)
	}
	return nil
}

func (a *App) UnblockPeer(peerID string) error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}
	if err := a.store.UnblockPeer(peerID); err != nil {
		return fmt.Errorf("UnblockPeer: %w", err)
	}
	return nil
}

func (a *App) ListBlocked() []string {
	if a.store == nil {
		return []string{}
	}
	list, err := a.store.ListBlocked()
	if err != nil {
		if a.ctx != nil {
			runtime.LogErrorf(a.ctx, "ListBlocked failed: %v", err)
		}
		return []string{}
	}
	return list
}

func (a *App) AddManualPeer(ipOrAddr string) error {
	if a.discovery == nil {
		return fmt.Errorf("discovery not initialized")
	}
	if ipOrAddr == "" {
		return fmt.Errorf("address is required")
	}

	addr := ipOrAddr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(ipOrAddr, discovery.UDPPort)
	}

	a.discovery.AddManualTarget(addr)
	return nil
}

func (a *App) RemoveManualPeer(ipOrAddr string) error {
	if a.discovery == nil {
		return fmt.Errorf("discovery not initialized")
	}
	if ipOrAddr == "" {
		return fmt.Errorf("address is required")
	}

	addr := ipOrAddr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(ipOrAddr, discovery.UDPPort)
	}

	a.discovery.RemoveManualTarget(addr)
	return nil
}

func (a *App) SendSignal(peerID, callID, kind, data string, video bool) error {
	if a.discovery == nil {
		return fmt.Errorf("discovery not initialized")
	}
	if a.transport == nil {
		return fmt.Errorf("transport not initialized")
	}
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}
	if callID == "" {
		return fmt.Errorf("callID is required")
	}
	if !models.IsAllowedSignalKind(kind) {
		return fmt.Errorf("unsupported signal kind: %s", kind)
	}
	if peerID == profile.PeerID {
		return fmt.Errorf("cannot send signal to self")
	}

	if _, lan := a.resolvePeer(peerID); !lan {
		if _, overlay := a.overlayMember(peerID); !overlay {
			return fmt.Errorf("peer %s is not reachable", peerID)
		}
	}

	payload, err := json.Marshal(models.SignalPayload{
		CallID: callID,
		Kind:   kind,
		Data:   data,
		Video:  video,
	})
	if err != nil {
		return fmt.Errorf("marshal SignalPayload: %w", err)
	}

	senderID := profile.PeerID
	go func() {
		if err := a.deliver(peerID, models.WireEnvelope{
			Type:     models.EnvelopeTypeSignal,
			SenderID: senderID,
			Payload:  payload,
		}); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "Send signal (%s) to %s failed: %v", kind, peerID, err)
			}
			a.emitEvent("signal:send_error", map[string]string{
				"peerId": peerID,
				"callId": callID,
				"kind":   kind,
				"error":  err.Error(),
			})
		}
	}()

	return nil
}

func (a *App) SendTyping(peerID string, isTyping bool) error {
	if a.discovery == nil || a.transport == nil {
		return fmt.Errorf("discovery/transport not initialized")
	}
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if peerID == "" {
		return fmt.Errorf("peerID is required")
	}
	if peerID == profile.PeerID {
		return nil
	}

	payload, err := json.Marshal(models.TypingPayload{IsTyping: isTyping})
	if err != nil {
		return fmt.Errorf("marshal TypingPayload: %w", err)
	}

	senderID := profile.PeerID
	go func() {
		if err := a.deliver(peerID, models.WireEnvelope{
			Type:     models.EnvelopeTypeTyping,
			SenderID: senderID,
			Payload:  payload,
		}); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "Send typing to %s failed: %v", peerID, err)
		}
	}()

	return nil
}

func (a *App) SendPing(peerID string) error {
	if a.transport == nil {
		return fmt.Errorf("transport not initialized")
	}
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}

	peer, ok := a.resolvePeer(peerID)
	if !ok {
		return fmt.Errorf("peer %s not found", peerID)
	}
	payload, _ := json.Marshal(models.PingPayload{SentAt: time.Now().UnixMilli()})
	env := models.WireEnvelope{
		Type:     models.EnvelopeTypePing,
		SenderID: profile.PeerID,
		Payload:  payload,
	}
	return a.transport.Send(peer, env)
}

func (a *App) ReactToMessage(peerID, messageID, emoji string) error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	profile := a.getProfile()
	if profile == nil {
		return fmt.Errorf("no active profile")
	}
	if messageID == "" {
		return fmt.Errorf("messageID is required")
	}

	if err := a.store.SetMessageReaction(messageID, emoji, true); err != nil {
		return fmt.Errorf("SetMessageReaction: %w", err)
	}

	if peerID == "" || a.transport == nil {
		return nil
	}

	payload, err := json.Marshal(models.ReactionPayload{
		MessageID: messageID,
		Emoji:     emoji,
	})
	if err != nil {
		return fmt.Errorf("marshal ReactionPayload: %w", err)
	}

	return a.deliver(peerID, models.WireEnvelope{
		Type:     models.EnvelopeTypeReaction,
		SenderID: profile.PeerID,
		Payload:  payload,
	})
}

func (a *App) handleEnvelope(env models.WireEnvelope) {
	if a.store == nil {
		return
	}
	if env.SenderID == "" {
		if a.ctx != nil {
			runtime.LogWarningf(a.ctx, "dropping envelope with empty sender: %+v", env)
		}
		return
	}

	profile := a.getProfile()
	if profile != nil && env.SenderID == profile.PeerID {
		return
	}
	if a.store.IsBlocked(env.SenderID) && env.Type != models.EnvelopeTypeAccountDeleted {
		return
	}

	switch env.Type {
	case models.EnvelopeTypePing:
		var p models.PingPayload
		_ = json.Unmarshal(env.Payload, &p)
		pong, _ := json.Marshal(models.PingPayload{SentAt: p.SentAt})
		if profile != nil {
			_ = a.deliver(env.SenderID, models.WireEnvelope{
				Type:     models.EnvelopeTypePong,
				SenderID: profile.PeerID,
				Payload:  pong,
			})
		}

	case models.EnvelopeTypePong:
		var p models.PingPayload
		_ = json.Unmarshal(env.Payload, &p)
		rtt := time.Now().UnixMilli() - p.SentAt
		a.emitEvent("ping:result", map[string]interface{}{
			"peerId": env.SenderID,
			"ms":     rtt,
		})

	case models.EnvelopeTypeMessage:
		var p models.MessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal MessagePayload failed: %v", err)
			}
			return
		}

		msg := models.Message{
			ID:        p.ID,
			ChatID:    env.SenderID,
			SenderID:  env.SenderID,
			Text:      p.Text,
			MediaKind: p.MediaKind,
			MediaData: p.MediaData,
			Timestamp: p.Timestamp,
		}

		a.ensureChatMeta(env.SenderID)

		if err := a.store.InsertMessage(msg); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "InsertMessage (incoming) failed: %v", err)
			}
			return
		}

		preview := mediaPreview(p.Text, p.MediaKind)
		if err := a.store.TouchChatLastMessage(env.SenderID, preview, p.Timestamp); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "TouchChatLastMessage incoming failed: %v", err)
		}

		a.emitEvent("message:incoming", msg)

		// Peer is clearly reachable now — flush anything we queued for them.
		go a.flushUndelivered()

	case models.EnvelopeTypeDeleteMessage:
		var p models.DeletePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal DeletePayload failed: %v", err)
			}
			return
		}
		if err := a.store.SoftDeleteMessage(p.ID, true); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "SoftDeleteMessage incoming failed: %v", err)
		}
		a.emitEvent("message:deleted", map[string]string{
			"chatId": env.SenderID,
			"id":     p.ID,
		})

	case models.EnvelopeTypeReadReceipt:
		var p models.ReadReceiptPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal ReadReceiptPayload failed: %v", err)
			}
			return
		}
		if err := a.store.MarkMessagesReadByIDs(p.MessageIDs); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "MarkMessagesReadByIDs failed: %v", err)
		}
		a.emitEvent("message:read", map[string]interface{}{
			"chatId": env.SenderID,
			"ids":    p.MessageIDs,
		})

	case models.EnvelopeTypeTyping:
		var p models.TypingPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal TypingPayload failed: %v", err)
			}
			return
		}
		a.emitEvent("signal:incoming", map[string]interface{}{
			"peerId": env.SenderID,
			"kind":   "typing",
			"data":   mustMarshalTyping(p),
		})

	case models.EnvelopeTypeReaction:
		var p models.ReactionPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal ReactionPayload failed: %v", err)
			}
			return
		}
		if err := a.store.SetMessageReaction(p.MessageID, p.Emoji, false); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "SetMessageReaction incoming failed: %v", err)
		}
		a.emitEvent("message:reacted", map[string]interface{}{
			"chatId":       env.SenderID,
			"id":           p.MessageID,
			"reactionPeer": p.Emoji,
		})

	case models.EnvelopeTypeProfileUpdate:
		var p models.ProfileUpdatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal ProfileUpdatePayload failed: %v", err)
			}
			return
		}
		if err := a.store.UpsertChatMetaIfExists(models.ChatMeta{
			PeerID: env.SenderID, Name: p.Name, Username: p.Username, Bio: p.Bio,
			Avatar: p.Avatar, Background: p.Background, Pattern: p.Pattern,
		}); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "UpsertChatMetaIfExists failed: %v", err)
		}
		if a.discovery != nil {
			a.discovery.UpdatePeerProfile(env.SenderID, p.Name, p.Username, p.Bio, p.Avatar)
		}
		a.emitEvent("profile:updated", map[string]string{
			"peerId":   env.SenderID,
			"name":     p.Name,
			"username": p.Username,
			"bio":      p.Bio,
			"avatar":   p.Avatar,
		})

	case models.EnvelopeTypeAvatarRequest:
		if a.transport == nil {
			return
		}
		myProfile := a.getProfile()
		if myProfile == nil {
			return
		}
		payload, err := json.Marshal(models.AvatarResponsePayload{
			Name:       myProfile.Name,
			Username:   myProfile.Username,
			Bio:        myProfile.Bio,
			Avatar:     myProfile.Avatar,
			Background: myProfile.Background,
			Pattern:    myProfile.Pattern,
		})
		if err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "marshal AvatarResponsePayload failed: %v", err)
			}
			return
		}
		_ = a.deliver(env.SenderID, models.WireEnvelope{
			Type:     models.EnvelopeTypeAvatarResponse,
			SenderID: myProfile.PeerID,
			Payload:  payload,
		})

	case models.EnvelopeTypeAvatarResponse:
		var p models.AvatarResponsePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal AvatarResponsePayload failed: %v", err)
			}
			return
		}
		if err := a.store.UpsertChatMeta(models.ChatMeta{
			PeerID: env.SenderID, Name: p.Name, Username: p.Username, Bio: p.Bio,
			Avatar: p.Avatar, Background: p.Background, Pattern: p.Pattern,
		}); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "UpsertChatMeta failed: %v", err)
		}
		if a.discovery != nil {
			a.discovery.UpdatePeerProfile(env.SenderID, p.Name, p.Username, p.Bio, p.Avatar)
		}
		a.emitEvent("profile:updated", map[string]string{
			"peerId":   env.SenderID,
			"name":     p.Name,
			"username": p.Username,
			"bio":      p.Bio,
			"avatar":   p.Avatar,
		})

	case models.EnvelopeTypeAccountDeleted:
		if err := a.store.MarkAccountDeleted(env.SenderID); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "MarkAccountDeleted failed: %v", err)
		}
		a.emitEvent("account:deleted", map[string]string{
			"peerId": env.SenderID,
		})

	case models.EnvelopeTypeSignal:
		var p models.SignalPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal SignalPayload failed: %v", err)
			}
			return
		}
		if p.CallID == "" {
			if a.ctx != nil {
				runtime.LogWarningf(a.ctx, "dropping signal with empty callID from %s", env.SenderID)
			}
			return
		}
		if !models.IsAllowedSignalKind(p.Kind) {
			if a.ctx != nil {
				runtime.LogWarningf(a.ctx, "dropping signal with unsupported kind %q from %s", p.Kind, env.SenderID)
			}
			return
		}
		a.emitEvent("signal:incoming", map[string]interface{}{
			"peerId": env.SenderID,
			"peerIp": a.peerIP(env.SenderID),
			"callId": p.CallID,
			"kind":   p.Kind,
			"data":   p.Data,
			"video":  p.Video,
		})

	default:
		if a.ctx != nil {
			runtime.LogWarningf(a.ctx, "unknown envelope type: %s", env.Type)
		}
	}
}

func mustMarshalTyping(p models.TypingPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return `{"isTyping":false}`
	}
	return string(b)
}

// SaveMedia writes a data: URL to a file the user picks. WKWebView ignores the
// HTML download attribute for data URLs, so the in-chat download button did
// nothing on macOS; going through a native save dialog works on both platforms.
// Returns the chosen path, or "" if the user cancelled.
func (a *App) SaveMedia(suggestedName, dataURL string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("app not started")
	}
	if dataURL == "" {
		return "", fmt.Errorf("no data to save")
	}

	comma := strings.Index(dataURL, ",")
	if !strings.HasPrefix(dataURL, "data:") || comma < 0 {
		return "", fmt.Errorf("unsupported data url")
	}
	meta, payload := dataURL[5:comma], dataURL[comma+1:]

	var raw []byte
	if strings.Contains(meta, ";base64") {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", fmt.Errorf("decode media: %w", err)
		}
		raw = decoded
	} else {
		raw = []byte(payload)
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename:      suggestedName,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // cancelled
	}

	if err := os.WriteFile(path, raw, 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// profileFileVersion is the on-disk format of an exported profile. Bump it only
// for a change a reader could not otherwise cope with.
const profileFileVersion = 1

// profileFile is what ExportProfile writes and ImportProfile reads. The wrapper
// exists so the file can be recognised before it is trusted, and so a later
// version can carry more than the profile without breaking old readers.
type profileFile struct {
	Kind       string         `json:"cloudix"`
	Version    int            `json:"version"`
	ExportedAt int64          `json:"exportedAt"`
	Profile    models.Profile `json:"profile"`
}

// ExportProfile writes the local profile to a file the user picks and returns
// the path, or "" if they cancelled.
//
// The peer id is part of the export on purpose: restoring the same identity is
// the whole point, since there is no account server to prove who you are and
// contacts recognise you by that id alone.
func (a *App) ExportProfile() (string, error) {
	profile := a.getProfile()
	if profile == nil {
		return "", fmt.Errorf("no active profile")
	}
	if a.ctx == nil {
		return "", fmt.Errorf("app not started")
	}

	data, err := json.MarshalIndent(profileFile{
		Kind:       "profile",
		Version:    profileFileVersion,
		ExportedAt: time.Now().Unix(),
		Profile:    *profile,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal profile: %w", err)
	}

	suggested := profile.Username
	if suggested == "" {
		suggested = profile.Name
	}
	if suggested == "" {
		suggested = "cloudix"
	}
	suggested = safeFileName(suggested) + ".cloudix-profile.json"

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename:      suggested,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // cancelled
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// ImportProfile reads a profile file the user picks and adopts it as the local
// profile. Returns nil if the user cancelled.
//
// This replaces whatever profile exists, so the UI must only offer it where
// there is nothing to lose — onboarding — or after warning.
func (a *App) ImportProfile() (*models.Profile, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if a.ctx == nil {
		return nil, fmt.Errorf("app not started")
	}

	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Cloudix",
		Filters: []runtime.FileFilter{
			{DisplayName: "Cloudix profile (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	if path == "" {
		return nil, nil // cancelled
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// A profile carries a base64 avatar, so the file is small but not tiny;
	// anything past a few megabytes is not one of ours.
	if len(raw) > 32<<20 {
		return nil, fmt.Errorf("file is too large to be a profile")
	}

	var file profileFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("not a Cloudix profile file")
	}
	if file.Kind != "profile" || file.Version < 1 {
		return nil, fmt.Errorf("not a Cloudix profile file")
	}
	if file.Version > profileFileVersion {
		return nil, fmt.Errorf("this profile was exported by a newer version of Cloudix")
	}

	p := file.Profile
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, fmt.Errorf("the profile has no name")
	}
	p.Username = normalizeUsername(p.Username)
	// A missing or malformed id would make the user invisible to everyone who
	// already knows them, which is the opposite of what an import is for — but
	// a fresh one still beats refusing to import at all.
	if !strings.HasPrefix(p.PeerID, "P-") {
		p.PeerID = genPeerID()
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}

	if err := a.store.SaveProfile(p); err != nil {
		return nil, fmt.Errorf("SaveProfile: %w", err)
	}
	a.setProfile(&p)
	return &p, nil
}

// safeFileName strips what a file name cannot contain on Windows or macOS, so a
// display name with a slash or a colon does not fail the save.
func safeFileName(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '-'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > 60 {
		cleaned = cleaned[:60]
	}
	if cleaned == "" {
		return "cloudix"
	}
	return cleaned
}

// GetCallLog returns the call history, newest first.
func (a *App) GetCallLog() []models.CallEntry {
	if a.store == nil {
		return []models.CallEntry{}
	}
	entries, err := a.store.ListCalls(200)
	if err != nil {
		a.logErr("ListCalls failed: %v", err)
		return []models.CallEntry{}
	}
	return entries
}

// LogCall records a finished call. The frontend owns call state, so it decides
// when a call is over and what became of it.
func (a *App) LogCall(id, peerID, name, direction, outcome string, video bool, duration int64) error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	switch direction {
	case models.CallIncoming, models.CallOutgoing:
	default:
		return fmt.Errorf("unknown call direction %q", direction)
	}
	switch outcome {
	case models.CallAccepted, models.CallDeclined, models.CallMissed:
	default:
		return fmt.Errorf("unknown call outcome %q", outcome)
	}
	if peerID == "" {
		return fmt.Errorf("peerId is required")
	}
	if id == "" {
		id = genPeerID()
	}
	if duration < 0 {
		duration = 0
	}
	entry := models.CallEntry{
		ID: id, PeerID: peerID, Name: name, Direction: direction,
		Outcome: outcome, Video: video, Duration: duration,
		Timestamp: time.Now().UnixMilli(),
	}
	if err := a.store.InsertCall(entry); err != nil {
		return fmt.Errorf("InsertCall: %w", err)
	}
	a.emitEvent("calls:update", entry)
	return nil
}

// ClearCallLog empties the call history.
func (a *App) ClearCallLog() error {
	if a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if err := a.store.ClearCalls(); err != nil {
		return fmt.Errorf("ClearCalls: %w", err)
	}
	a.emitEvent("calls:cleared", nil)
	return nil
}

// AppVersion is shown in Settings so a user can tell at a glance which build
// they're running (stale side-by-side installs are otherwise invisible).
func (a *App) AppVersion() string { return appVersion }

// GetDataDir returns the folder holding the local profile/chat database.
func (a *App) GetDataDir() string { return storage.DataDir() }

// OpenDataFolder reveals the local data folder in the OS file manager.
func (a *App) OpenDataFolder() error {
	dir := storage.DataDir()

	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer.exe", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}

	// explorer.exe exits with a non-zero status even when it succeeds.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (a *App) RestartNetworking() error {
	if a.discovery == nil {
		return fmt.Errorf("discovery not initialized")
	}
	if err := a.discovery.Restart(); err != nil {
		return fmt.Errorf("discovery.Restart: %w", err)
	}
	// Sockets usually survive a Wi-Fi switch or a VPN toggle while being
	// useless afterwards, so the overlay is rebuilt from scratch rather than
	// trusted to notice on its own.
	if svc := a.vpnService(); svc != nil {
		go svc.Reconnect()
	}
	if a.ctx != nil {
		runtime.LogInfof(a.ctx, "networking restarted after connectivity change")
	}
	return nil
}
