package protocol

import (
	"encoding/json"
	"time"
)
type Envelope struct {
	Type     string          `json:"type"`
	SenderID int             `json:"sender_id"`
	Version  int             `json:"version,omitempty"`
	TxID     string          `json:"tx_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}
type HelloPayload struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Balance int    `json:"balance"`
	Version int    `json:"version"`
}
type NetworkStatePayload struct {
	Nodes     []NodeInfo `json:"nodes"`
	Timestamp time.Time  `json:"timestamp"`
}
type NodeInfo struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Balance int    `json:"balance"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}
type StateUpdatePayload struct {
	Balance int `json:"balance"`
	Version int `json:"version"`
}
type AckPayload struct {
	MessageType string `json:"message_type"`
	Success     bool   `json:"success"`
	Message     string `json:"message,omitempty"`
}
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

type LockRequestPayload struct {
	TxID string `json:"tx_id"`
}

type LockGrantedPayload struct {
	TxID  string `json:"tx_id"`
	TTLMs int    `json:"ttl_ms"`
}

type TransactionPayload struct {
	From    int `json:"from"`
	To      int `json:"to"`
	Amount  int `json:"amount"`
	Version int `json:"version"`
}

type CommitNoticePayload struct {
	TxID string `json:"tx_id"`
}

type StatePushRequestPayload struct {
	TxID string `json:"tx_id"`
}

type StatePushResponsePayload struct {
	Balances map[int]int `json:"balances"`
	Version  int         `json:"version"`
}

const (
	MsgTypeHello            = "hello"
	MsgTypeStateUpdate      = "state_update"
	MsgTypeNetworkState     = "network_state"
	MsgTypeAck              = "ack"
	MsgTypeError            = "error"
	MsgTypeLockRequest      = "lock_request"
	MsgTypeLockGranted      = "lock_granted"
	MsgTypeTransaction       = "transaction"
	MsgTypeCommitNotice     = "commit_notice"
	MsgTypeStatePushRequest = "state_push"
	MsgTypeStatePushResponse = "state_push_response"
	MsgTypeLockRelease      = "lock_release"
)
func CreateEnvelope(msgType string, senderID int, payload interface{}) (*Envelope, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return &Envelope{
		Type:     msgType,
		SenderID: senderID,
		Payload:  payloadBytes,
	}, nil
}
func (e *Envelope) ParsePayload(target interface{}) error {
	return json.Unmarshal(e.Payload, target)
}
