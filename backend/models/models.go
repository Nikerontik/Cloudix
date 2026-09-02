package models

import "encoding/json"

const (
	EnvelopeTypeMessage        = "message"
	EnvelopeTypeDeleteMessage  = "delete_message"
	EnvelopeTypeReadReceipt    = "read_receipt"
	EnvelopeTypeProfileUpdate  = "profile_update"
	EnvelopeTypeAvatarRequest  = "avatar_request"
	EnvelopeTypeAvatarResponse = "avatar_response"
	EnvelopeTypeAccountDeleted = "account_deleted"
	EnvelopeTypeSignal         = "signal"
	EnvelopeTypeTyping         = "typing"
	EnvelopeTypeReaction       = "reaction"
	EnvelopeTypePing           = "ping"
	EnvelopeTypePong           = "pong"
)

// Locale-neutral chat-list previews for media messages. The frontend maps these
// to `t.mediaPreview.*`; storing a localized string in the DB used to leak
// Russian text into the English UI.
const (
	PreviewImage = "[[image]]"
	PreviewVideo = "[[video]]"
	PreviewFile  = "[[file]]"
)

const (
	SignalKindOffer             = "offer"
	SignalKindAnswer            = "answer"
	SignalKindICE               = "ice"
	SignalKindEnd               = "end"
	SignalKindReject            = "reject"
	SignalKindRenegotiateOffer  = "renegotiate-offer"
	SignalKindRenegotiateAnswer = "renegotiate-answer"
	// Screen share announces which inbound track ids carry the shared screen,
	// so the receiver can route them to the dedicated viewer instead of the
	// camera surface. WebRTC itself carries no such labelling.
	SignalKindScreenOn  = "screen-on"
	SignalKindScreenOff = "screen-off"
)

type Profile struct {
	PeerID   string `json:"peerId"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"`
	Avatar   string `json:"avatar"`
	// Background and Pattern decorate the profile card and travel with the
	// profile, so a peer sees the same header the owner chose. Both are short
	// identifiers ("teal", "gradient-rose", "bottles"), never colour literals —
	// the actual colours live in theme.css and follow the active theme.
	Background string `json:"background"`
	Pattern    string `json:"pattern"`
	CreatedAt  int64  `json:"createdAt"`
}

type Peer struct {
	PeerID     string `json:"peerId"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Bio        string `json:"bio"`
	Avatar     string `json:"avatar"`
	Background string `json:"background"`
	Pattern    string `json:"pattern"`
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	LastSeen   int64  `json:"lastSeen"`
	// ViaVPN marks a peer reached through the overlay network rather than the
	// local network, so the UI can label it.
	ViaVPN bool `json:"viaVpn,omitempty"`
}

type Message struct {
	ID             string `json:"id"`
	ChatID         string `json:"chatId"`
	SenderID       string `json:"senderId"`
	Text           string `json:"text"`
	MediaKind      string `json:"mediaKind,omitempty"`
	MediaData      string `json:"mediaData,omitempty"`
	Timestamp      int64  `json:"ts"`
	DeletedForMe   bool   `json:"deletedForMe"`
	DeletedForBoth bool   `json:"deletedForBoth"`
	Read           bool   `json:"read"`
	Reaction       string `json:"reaction,omitempty"`     // моя реакция
	ReactionPeer   string `json:"reactionPeer,omitempty"` // реакция собеседника
	Delivered      bool   `json:"delivered"`
}

type Chat struct {
	PeerID         string `json:"peerId"`
	Name           string `json:"name"`
	Username       string `json:"username"`
	Bio            string `json:"bio"`
	Avatar         string `json:"avatar"`
	Background     string `json:"background"`
	Pattern        string `json:"pattern"`
	AccountDeleted bool   `json:"accountDeleted"`
	LastMessage    string `json:"lastMessage"`
	LastTimestamp  int64  `json:"lastTimestamp"`
	Unread         int    `json:"unread"`
}

type WireEnvelope struct {
	Type     string          `json:"type"`
	SenderID string          `json:"senderId"`
	Payload  json.RawMessage `json:"payload"`
}

type MessagePayload struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	MediaKind string `json:"mediaKind,omitempty"`
	MediaData string `json:"mediaData,omitempty"`
	Timestamp int64  `json:"ts"`
}

type DeletePayload struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

type ProfileUpdatePayload struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	Bio        string `json:"bio"`
	Avatar     string `json:"avatar"`
	Background string `json:"background"`
	Pattern    string `json:"pattern"`
}

type AvatarRequestPayload struct{}

type AvatarResponsePayload struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	Bio        string `json:"bio"`
	Avatar     string `json:"avatar"`
	Background string `json:"background"`
	Pattern    string `json:"pattern"`
}

type ReadReceiptPayload struct {
	MessageIDs []string `json:"messageIds"`
}

// FIX: добавлены Name/Username. Раньше при входящем звонке до момента полной
// синхронизации профиля через discovery/avatar-обмен на экране входящего
// звонка показывался PeerID вместо имени собеседника — потому что больше
// негде было взять имя, если discovery ещё не знает пира (частый случай при
// асимметричном multicast). Теперь имя/юзернейм звонящего едут прямо в
// сигнале offer, независимо от состояния discovery.
type SignalPayload struct {
	CallID   string `json:"callId"`
	Kind     string `json:"kind"`
	Data     string `json:"data"`
	Video    bool   `json:"video"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

type TypingPayload struct {
	IsTyping bool `json:"isTyping"`
}

type PingPayload struct {
	SentAt int64 `json:"sentAt"`
}

type ReactionPayload struct {
	MessageID string `json:"messageId"`
	Emoji     string `json:"emoji"`
}

func IsAllowedSignalKind(kind string) bool {
	switch kind {
	case SignalKindOffer,
		SignalKindAnswer,
		SignalKindICE,
		SignalKindEnd,
		SignalKindReject,
		SignalKindRenegotiateOffer,
		SignalKindRenegotiateAnswer,
		SignalKindScreenOn,
		SignalKindScreenOff:
		return true
	default:
		return false
	}
}

// ChatMeta is the peer-supplied half of a chat row — everything that arrives in
// a profile_update or an avatar_response. It travels as one struct so adding a
// field later does not ripple through every call site.
type ChatMeta struct {
	PeerID     string `json:"peerId"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Bio        string `json:"bio"`
	Avatar     string `json:"avatar"`
	Background string `json:"background"`
	Pattern    string `json:"pattern"`
}

// Call log directions.
const (
	CallIncoming = "incoming"
	CallOutgoing = "outgoing"
)

// Call outcomes. A call that was answered is "accepted"; one the peer declined
// is "declined"; one that rang out or was cancelled is "missed".
const (
	CallAccepted = "accepted"
	CallDeclined = "declined"
	CallMissed   = "missed"
)

// CallEntry is one row of the call log.
type CallEntry struct {
	ID        string `json:"id"`
	PeerID    string `json:"peerId"`
	Name      string `json:"name"`
	Direction string `json:"direction"`
	Outcome   string `json:"outcome"`
	Video     bool   `json:"video"`
	// Seconds the call was connected; 0 for anything never answered.
	Duration  int64 `json:"duration"`
	Timestamp int64 `json:"ts"`
}
