package protocol

import (
	"encoding/json"
	"time"
)

// Envelope - общая оболочка для всех сообщений
type Envelope struct {
	Type     string          `json:"type"`
	SenderID int             `json:"sender_id"`
	Version  int             `json:"version,omitempty"`
	TxID     string          `json:"tx_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// HelloPayload - сообщение при подключении кошелька к контроллеру
type HelloPayload struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Balance int    `json:"balance"`
	Version int    `json:"version"`
}

// NetworkStatePayload - состояние сети для веб-клиента
type NetworkStatePayload struct {
	Nodes     []NodeInfo `json:"nodes"`
	Timestamp time.Time  `json:"timestamp"`
}

// NodeInfo - информация об узле в сети
type NodeInfo struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Balance int    `json:"balance"`
	Version int    `json:"version"`
	Status  string `json:"status"` // "online" или "offline"
}

// StateUpdatePayload - обновление локального состояния
type StateUpdatePayload struct {
	Balance int `json:"balance"`
	Version int `json:"version"`
}

// AckPayload - подтверждение получения сообщения
type AckPayload struct {
	MessageType string `json:"message_type"`
	Success     bool   `json:"success"`
	Message     string `json:"message,omitempty"`
}

// ErrorPayload - сообщение об ошибке
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Константы типов сообщений для этапа 1
const (
	MsgTypeHello        = "hello"
	MsgTypeStateUpdate  = "state_update"
	MsgTypeNetworkState = "network_state"
	MsgTypeAck          = "ack"
	MsgTypeError        = "error"
)

// CreateEnvelope - создает новый Envelope с заданными параметрами
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

// ParsePayload - парсит payload из envelope в указанную структуру
func (e *Envelope) ParsePayload(target interface{}) error {
	return json.Unmarshal(e.Payload, target)
}
