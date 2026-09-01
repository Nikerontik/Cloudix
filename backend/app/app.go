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

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// appVersion is surfaced in Settings; bump it with user-visible releases.
const appVersion = "0.2.0"

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
		// Only retry when the peer is actually reachable right now: a live
		// discovery entry or an open connection. A stale entry would make
		// transport.Send burn its full dial timeout per queued message.
		live := false
		if a.discovery != nil {
			_, live = a.discovery.GetPeer(m.ChatID)
		}
		if !live && !transportMgr.HasConn(m.ChatID) {
			continue
		}
		peer, ok := a.resolvePeer(m.ChatID)
		if !ok {
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
		if err := transportMgr.Send(peer, models.WireEnvelope{
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
}

func (a *App) OnBeforeClose(ctx context.Context) bool {
	profile := a.getProfile()
	if profile != nil && a.discovery != nil {
		a.discovery.AnnounceGoodbye(profile)
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
		Username:  username,
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
	updated.Username = p.Username
	updated.Bio = p.Bio
	updated.Avatar = p.Avatar

	if err := a.store.SaveProfile(updated); err != nil {
		return fmt.Errorf("SaveProfile: %w", err)
	}
	a.setProfile(&updated)

	payload, err := json.Marshal(models.ProfileUpdatePayload{
		Name:     updated.Name,
		Username: updated.Username,
		Bio:      updated.Bio,
		Avatar:   updated.Avatar,
	})
	if err != nil {
		return fmt.Errorf("marshal ProfileUpdatePayload: %w", err)
	}

	if a.discovery != nil && a.transport != nil {
		for _, peer := range a.discovery.ListPeers() {
			if err := a.transport.Send(peer, models.WireEnvelope{
				Type:     models.EnvelopeTypeProfileUpdate,
				SenderID: updated.PeerID,
				Payload:  payload,
			}); err != nil && a.ctx != nil {
				runtime.LogErrorf(a.ctx, "profile_update send to %s failed: %v", peer.PeerID, err)
			}
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

func (a *App) GetOnlinePeers() []models.Peer {
	if a.discovery == nil || a.store == nil {
		return []models.Peer{}
	}
	all := a.discovery.ListPeers()
	out := make([]models.Peer, 0, len(all))
	for _, p := range all {
		if !a.store.IsBlocked(p.PeerID) {
			out = append(out, p)
		}
	}
	return out
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

	peer, ok := a.resolvePeer(msg.ChatID)
	if !ok {
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

	if err := transportMgr.Send(peer, models.WireEnvelope{
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

	if a.transport != nil {
		if peer, ok := a.resolvePeer(peerID); ok {
			payload, err := json.Marshal(models.ReadReceiptPayload{MessageIDs: ids})
			if err != nil {
				return fmt.Errorf("marshal ReadReceiptPayload: %w", err)
			}
			if err := a.transport.Send(peer, models.WireEnvelope{
				Type:     models.EnvelopeTypeReadReceipt,
				SenderID: profile.PeerID,
				Payload:  payload,
			}); err != nil && a.ctx != nil {
				runtime.LogErrorf(a.ctx, "Send read_receipt failed: %v", err)
			}
		}
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

	if forBoth && a.transport != nil {
		if peer, ok := a.resolvePeer(peerID); ok {
			payload, err := json.Marshal(models.DeletePayload{ID: id, Mode: mode})
			if err == nil {
				_ = a.transport.Send(peer, models.WireEnvelope{
					Type:     models.EnvelopeTypeDeleteMessage,
					SenderID: profile.PeerID,
					Payload:  payload,
				})
			}
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

func (a *App) StartChatWithPeer(peerID, name, username, bio, avatar string) error {
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

	if err := a.store.UpsertChatMeta(peerID, name, username, bio, avatar); err != nil {
		return fmt.Errorf("UpsertChatMeta: %w", err)
	}

	if a.transport != nil {
		if peer, ok := a.resolvePeer(peerID); ok {
			payload, err := json.Marshal(models.AvatarRequestPayload{})
			if err == nil {
				_ = a.transport.Send(peer, models.WireEnvelope{
					Type:     models.EnvelopeTypeAvatarRequest,
					SenderID: profile.PeerID,
					Payload:  payload,
				})
			}
		}
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

	peer, ok := a.resolvePeer(peerID)
	if !ok {
		return fmt.Errorf("peer %s not found in local network", peerID)
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

	transportMgr := a.transport
	senderID := profile.PeerID
	go func() {
		if err := transportMgr.Send(peer, models.WireEnvelope{
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

	peer, ok := a.resolvePeer(peerID)
	if !ok {
		return nil
	}

	payload, err := json.Marshal(models.TypingPayload{IsTyping: isTyping})
	if err != nil {
		return fmt.Errorf("marshal TypingPayload: %w", err)
	}

	transportMgr := a.transport
	senderID := profile.PeerID
	go func() {
		if err := transportMgr.Send(peer, models.WireEnvelope{
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

	peer, ok := a.resolvePeer(peerID)
	if !ok {
		return nil
	}

	payload, err := json.Marshal(models.ReactionPayload{
		MessageID: messageID,
		Emoji:     emoji,
	})
	if err != nil {
		return fmt.Errorf("marshal ReactionPayload: %w", err)
	}

	return a.transport.Send(peer, models.WireEnvelope{
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
		if a.transport != nil {
			if peer, ok := a.resolvePeer(env.SenderID); ok && profile != nil {
				_ = a.transport.Send(peer, models.WireEnvelope{
					Type:     models.EnvelopeTypePong,
					SenderID: profile.PeerID,
					Payload:  pong,
				})
			}
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

		if a.discovery != nil {
			if peer, ok := a.discovery.GetPeerEvenIfStale(env.SenderID); ok {
				_ = a.store.UpsertChatMeta(env.SenderID, peer.Name, peer.Username, peer.Bio, peer.Avatar)
			}
		}

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
		if err := a.store.UpsertChatMetaIfExists(env.SenderID, p.Name, p.Username, p.Bio, p.Avatar); err != nil && a.ctx != nil {
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
		if peer, ok := a.resolvePeer(env.SenderID); ok {
			payload, err := json.Marshal(models.AvatarResponsePayload{
				Name:     myProfile.Name,
				Username: myProfile.Username,
				Bio:      myProfile.Bio,
				Avatar:   myProfile.Avatar,
			})
			if err != nil {
				if a.ctx != nil {
					runtime.LogErrorf(a.ctx, "marshal AvatarResponsePayload failed: %v", err)
				}
				return
			}
			_ = a.transport.Send(peer, models.WireEnvelope{
				Type:     models.EnvelopeTypeAvatarResponse,
				SenderID: myProfile.PeerID,
				Payload:  payload,
			})
		}

	case models.EnvelopeTypeAvatarResponse:
		var p models.AvatarResponsePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal AvatarResponsePayload failed: %v", err)
			}
			return
		}
		if err := a.store.UpsertChatMeta(env.SenderID, p.Name, p.Username, p.Bio, p.Avatar); err != nil && a.ctx != nil {
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
	if a.ctx != nil {
		runtime.LogInfof(a.ctx, "networking restarted after connectivity change")
	}
	return nil
}
