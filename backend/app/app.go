package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"cloudix/backend/discovery"
	"cloudix/backend/models"
	"cloudix/backend/storage"
	"cloudix/backend/transport"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx       context.Context
	store     *storage.Store
	discovery *discovery.Service
	transport *transport.Manager

	profileMu sync.RWMutex
	profile   *models.Profile
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

	a.transport = transport.NewManager(a.handleEnvelope)
	port, err := a.transport.Start()
	if err != nil {
		runtime.LogErrorf(ctx, "transport.Start failed: %v", err)
		return
	}

	a.discovery = discovery.NewService(
		port,
		a.getProfile,
		func() {
			if a.ctx != nil && a.discovery != nil {
				runtime.EventsEmit(a.ctx, "peers:update", a.discovery.ListPeers())
			}
		},
	)
	if err := a.discovery.Start(); err != nil {
		runtime.LogErrorf(ctx, "discovery.Start failed: %v", err)
	}
}

func (a *App) OnBeforeClose(ctx context.Context) bool {
	profile := a.getProfile()
	if profile != nil && a.discovery != nil {
		a.discovery.AnnounceGoodbye(profile)
	}
	if a.discovery != nil {
		a.discovery.Stop()
	}
	if a.transport != nil {
		a.transport.Stop()
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			runtime.LogErrorf(ctx, "store.Close failed: %v", err)
		}
	}
	return false
}

func genPeerID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "P-" + hex.EncodeToString(b)
}

func (a *App) emitEvent(name string, payload interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, name, payload)
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
			if peer, ok := a.discovery.GetPeer(c.PeerID); ok {
				_ = a.transport.Send(peer, models.WireEnvelope{
					Type:     models.EnvelopeTypeAccountDeleted,
					SenderID: profile.PeerID,
				})
			}
		}
		a.discovery.AnnounceGoodbye(profile)
	}

	if a.discovery != nil {
		a.discovery.Stop()
		a.discovery = nil
	}
	if a.transport != nil {
		a.transport.Stop()
		a.transport = nil
	}
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

	a.transport = transport.NewManager(a.handleEnvelope)
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
	)
	if err := a.discovery.Start(); err != nil {
		return fmt.Errorf("discovery.Start: %w", err)
	}

	return nil
}

// FIX: раньше метод назывался GetDiscoveredPeers, а фронт вызывал
// WailsApp.GetOnlinePeers (проверка typeof тихо проваливалась). Это была
// главная причина бага "новорег не видит уже онлайн собеседника" — весь
// повторный polling из App.jsx (таймеры 400мс..6с) не делал ничего, а
// единственное событие peers:update, отправленное ДО регистрации, терялось,
// потому что React-слушатель этого события подписывается только после
// появления профиля.
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
	}

	if err := a.store.InsertMessage(msg); err != nil {
		return nil, fmt.Errorf("InsertMessage: %w", err)
	}

	preview := text
	if mediaKind != "" {
		switch mediaKind {
		case "image":
			preview = "📷 Фото"
		case "video":
			preview = "🎥 Видео"
		default:
			preview = "📎 Вложение"
		}
	}
	if err := a.store.TouchChatLastMessage(peerID, preview, msg.Timestamp); err != nil && a.ctx != nil {
		runtime.LogErrorf(a.ctx, "TouchChatLastMessage failed: %v", err)
	}

	if a.discovery != nil && a.transport != nil {
		if peer, ok := a.discovery.GetPeer(peerID); ok {
			payload, err := json.Marshal(models.MessagePayload{
				ID:        msg.ID,
				Text:      text,
				MediaKind: mediaKind,
				MediaData: mediaData,
				Timestamp: msg.Timestamp,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal MessagePayload: %w", err)
			}
			if err := a.transport.Send(peer, models.WireEnvelope{
				Type:     models.EnvelopeTypeMessage,
				SenderID: profile.PeerID,
				Payload:  payload,
			}); err != nil && a.ctx != nil {
				runtime.LogErrorf(a.ctx, "Send message to peer failed: %v", err)
			}
		}
	}

	return &msg, nil
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

	if a.discovery != nil && a.transport != nil {
		if peer, ok := a.discovery.GetPeer(peerID); ok {
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

	if forBoth && a.discovery != nil && a.transport != nil {
		if peer, ok := a.discovery.GetPeer(peerID); ok {
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

	if a.discovery != nil && a.transport != nil {
		if peer, ok := a.discovery.GetPeer(peerID); ok {
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

// FIX: главная причина фриза UI при приёме/совершении звонка. SendSignal —
// bound-метод, вызываемый СИНХРОННО из фронта через Wails JS-мост. Раньше
// a.transport.Send(...) (который внутри может делать net.Dial с таймаутом
// в несколько секунд, если TCP-соединение с пиром разорвано) выполнялся
// прямо в этом вызове и держал в блоке весь мост, из-за чего весь UI
// зависал на время до ответа/таймаута Dial. Теперь валидация остаётся
// синхронной (она быстрая, без сети), а сама сетевая отправка уходит в
// отдельную горутину — метод возвращается фронту немедленно, а об ошибке
// отправки фронт узнаёт через асинхронное событие "signal:send_error".
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

	// FIX: используем GetPeerEvenIfStale, а не GetPeer — сигналы конца
	// звонка (end/reject) и ответ на входящий звонок должны доходить даже
	// если UDP-анонс временно не пришёл (см. комментарий в discovery.go).
	peer, ok := a.discovery.GetPeerEvenIfStale(peerID)
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

// NEW: индикатор "печатает…". Идёт отдельным конвертом (не через SendSignal,
// чтобы не иметь callID и не пересекаться с логикой звонков), но на фронте
// приходит через тот же канал "signal:incoming" с kind == "typing".
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

	peer, ok := a.discovery.GetPeer(peerID)
	if !ok {
		// Пир мог выйти из сети — это не ошибка, просто нечего отправлять.
		return nil
	}

	payload, err := json.Marshal(models.TypingPayload{IsTyping: isTyping})
	if err != nil {
		return fmt.Errorf("marshal TypingPayload: %w", err)
	}

	// FIX: как и SendSignal, отправка индикатора печати не должна блокировать
	// UI — это некритичный сигнал, потеря/задержка которого не страшна.
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

// NEW: реакции на сообщения. Сохраняем локально сразу (оптимистично на
// фронте это уже сделано), затем рассылаем собеседнику, если он в сети.
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

	if err := a.store.SetMessageReaction(messageID, emoji); err != nil {
		return fmt.Errorf("SetMessageReaction: %w", err)
	}

	if peerID == "" || a.discovery == nil || a.transport == nil {
		return nil
	}

	peer, ok := a.discovery.GetPeer(peerID)
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
			if peer, ok := a.discovery.GetPeer(env.SenderID); ok {
				_ = a.store.UpsertChatMeta(env.SenderID, peer.Name, peer.Username, peer.Bio, peer.Avatar)
			}
		}

		if err := a.store.InsertMessage(msg); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "InsertMessage (incoming) failed: %v", err)
			}
			return
		}

		preview := p.Text
		if p.MediaKind != "" {
			switch p.MediaKind {
			case "image":
				preview = "📷 Фото"
			case "video":
				preview = "🎥 Видео"
			default:
				preview = "📎 Вложение"
			}
		}
		if err := a.store.TouchChatLastMessage(env.SenderID, preview, p.Timestamp); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "TouchChatLastMessage incoming failed: %v", err)
		}

		a.emitEvent("message:incoming", msg)

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

	// NEW: индикатор "печатает…". Эмитим через тот же общий канал
	// signal:incoming, с kind == "typing" — фронт (App.jsx onSignalIncoming)
	// перехватывает эту ветку раньше, чем логику звонков.
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

	// NEW: реакция от собеседника.
	case models.EnvelopeTypeReaction:
		var p models.ReactionPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			if a.ctx != nil {
				runtime.LogErrorf(a.ctx, "unmarshal ReactionPayload failed: %v", err)
			}
			return
		}
		if err := a.store.SetMessageReaction(p.MessageID, p.Emoji); err != nil && a.ctx != nil {
			runtime.LogErrorf(a.ctx, "SetMessageReaction incoming failed: %v", err)
		}
		a.emitEvent("message:reacted", map[string]interface{}{
			"chatId":   env.SenderID,
			"id":       p.MessageID,
			"reaction": p.Emoji,
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
		if a.transport == nil || a.discovery == nil {
			return
		}
		myProfile := a.getProfile()
		if myProfile == nil {
			return
		}
		if peer, ok := a.discovery.GetPeer(env.SenderID); ok {
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
