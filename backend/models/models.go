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

const (
	SignalKindOffer             = "offer"
	SignalKindAnswer            = "answer"
	SignalKindICE               = "ice"
	SignalKindEnd               = "end"
	SignalKindReject            = "reject"
	SignalKindRenegotiateOffer  = "renegotiate-offer"
	SignalKindRenegotiateAnswer = "renegotiate-answer"
)

type Profile struct {
	PeerID    string `json:"peerId"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Bio       string `json:"bio"`
	Avatar    string `json:"avatar"`
	CreatedAt int64  `json:"createdAt"`
}

type Peer struct {
	PeerID   string `json:"peerId"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"`
	Avatar   string `json:"avatar"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	LastSeen int64  `json:"lastSeen"`
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
	Reaction       string `json:"reaction,omitempty"`
}

type Chat struct {
	PeerID         string `json:"peerId"`
	Name           string `json:"name"`
	Username       string `json:"username"`
	Bio            string `json:"bio"`
	Avatar         string `json:"avatar"`
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
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"`
	Avatar   string `json:"avatar"`
}

type AvatarRequestPayload struct{}

type AvatarResponsePayload struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"`
	Avatar   string `json:"avatar"`
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
		SignalKindRenegotiateAnswer:
		return true
	default:
		return false
	}
}
