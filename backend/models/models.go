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
	EnvelopeTypeTyping         = "typing"   // NEW: индикатор набора текста
	EnvelopeTypeReaction       = "reaction" // NEW: реакции на сообщения
)

const (
	SignalKindOffer              = "offer"
	SignalKindAnswer             = "answer"
	SignalKindICE                = "ice"
	SignalKindEnd                = "end"
	SignalKindReject             = "reject"
	SignalKindRenegotiateOffer   = "renegotiate-offer"
	SignalKindRenegotiateAnswer  = "renegotiate-answer"
	SignalKindScreenShareStarted = "screen-share-started" // NEW
	SignalKindScreenShareStopped = "screen-share-stopped"
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
	Reaction       string `json:"reaction,omitempty"` // NEW
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

type SignalPayload struct {
	CallID string `json:"callId"`
	Kind   string `json:"kind"`
	Data   string `json:"data"`
	Video  bool   `json:"video"`
}

// NEW: индикатор печатания
type TypingPayload struct {
	IsTyping bool `json:"isTyping"`
}

// NEW: реакция на сообщение
type ReactionPayload struct {
	MessageID string `json:"messageId"`
	Emoji     string `json:"emoji"` // пустая строка = снять реакцию
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
		SignalKindScreenShareStarted, // NEW
		SignalKindScreenShareStopped:
		return true
	default:
		return false
	}
}
