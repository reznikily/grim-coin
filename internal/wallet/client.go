package wallet

import (
	"context"
	"fmt"
	"grim-coin/internal/protocol"
	"grim-coin/internal/store"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Config - конфигурация кошелька
type Config struct {
	ID           int      `yaml:"id"`
	Name         string   `yaml:"name"`
	IP           string   `yaml:"ip"`
	ListenP3     string   `yaml:"listen_p3"`
	ControllerWS string   `yaml:"controller_ws"`
	Peers        []string `yaml:"peers"`
}

// Client - клиент кошелька
type Client struct {
	config     *Config
	ledger     *store.Ledger
	conn       *websocket.Conn
	mutex      sync.RWMutex
	connected  bool
	retryCount int
	maxRetries int
}

// NewClient - создает новый клиент кошелька
func NewClient(config *Config, dataPath string) (*Client, error) {
	// Создаем ledger
	ledger, err := store.NewLedger(dataPath, config.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger: %w", err)
	}

	return &Client{
		config:     config,
		ledger:     ledger,
		maxRetries: 3,
	}, nil
}

// Start - запускает кошелек
func (c *Client) Start(ctx context.Context) error {
	log.Printf("Starting wallet %d (%s)", c.config.ID, c.config.Name)

	// Запускаем подключение к контроллеру
	go c.connectToController(ctx)

	// Ожидаем завершения
	<-ctx.Done()

	log.Printf("Shutting down wallet %d", c.config.ID)
	c.disconnect()

	return nil
}

// connectToController - подключается к контроллеру с повторными попытками
func (c *Client) connectToController(ctx context.Context) {
	backoffDurations := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := c.connect(); err != nil {
			c.retryCount++
			log.Printf("Failed to connect to controller (attempt %d/%d): %v",
				c.retryCount, c.maxRetries, err)

			if c.retryCount >= c.maxRetries {
				log.Printf("Max retries exceeded, waiting before retry cycle...")
				time.Sleep(5 * time.Second)
				c.retryCount = 0
				continue
			}

			backoffIndex := c.retryCount - 1
			if backoffIndex >= len(backoffDurations) {
				backoffIndex = len(backoffDurations) - 1
			}

			time.Sleep(backoffDurations[backoffIndex])
			continue
		}

		// Успешное подключение
		c.retryCount = 0
		log.Printf("Successfully connected to controller")

		// Отправляем hello
		if err := c.sendHello(); err != nil {
			log.Printf("Failed to send hello: %v", err)
			c.disconnect()
			continue
		}

		// Читаем сообщения
		c.handleMessages(ctx)

		// Если дошли сюда, значит соединение разорвано
		log.Printf("Connection to controller lost, attempting to reconnect...")
		c.disconnect()
		time.Sleep(1 * time.Second)
	}
}

// connect - устанавливает WebSocket соединение с контроллером
func (c *Client) connect() error {
	u, err := url.Parse(c.config.ControllerWS)
	if err != nil {
		return fmt.Errorf("invalid controller URL: %w", err)
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to controller: %w", err)
	}

	c.mutex.Lock()
	c.conn = conn
	c.connected = true
	c.mutex.Unlock()

	return nil
}

// disconnect - разрывает соединение с контроллером
func (c *Client) disconnect() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connected = false
}

// sendHello - отправляет hello сообщение контроллеру
func (c *Client) sendHello() error {
	balance := c.ledger.GetBalance(c.config.ID)
	version := c.ledger.GetVersion()

	hello := protocol.HelloPayload{
		ID:      c.config.ID,
		Name:    c.config.Name,
		IP:      c.config.IP,
		Balance: balance,
		Version: version,
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeHello, c.config.ID, hello)
	if err != nil {
		return fmt.Errorf("failed to create hello envelope: %w", err)
	}

	c.mutex.RLock()
	conn := c.conn
	c.mutex.RUnlock()

	if conn == nil {
		return fmt.Errorf("no connection to controller")
	}

	if err := conn.WriteJSON(envelope); err != nil {
		return fmt.Errorf("failed to send hello: %w", err)
	}

	log.Printf("Sent hello: ID=%d, Name=%s, Balance=%d, Version=%d",
		hello.ID, hello.Name, hello.Balance, hello.Version)

	return nil
}

// handleMessages - обрабатывает входящие сообщения от контроллера
func (c *Client) handleMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.mutex.RLock()
		conn := c.conn
		c.mutex.RUnlock()

		if conn == nil {
			break
		}

		var envelope protocol.Envelope
		if err := conn.ReadJSON(&envelope); err != nil {
			log.Printf("Failed to read message from controller: %v", err)
			break
		}

		log.Printf("Received message type: %s", envelope.Type)

		switch envelope.Type {
		case protocol.MsgTypeAck:
			c.handleAckMessage(&envelope)
		case protocol.MsgTypeError:
			c.handleErrorMessage(&envelope)
		default:
			log.Printf("Unknown message type: %s", envelope.Type)
		}
	}
}

// handleAckMessage - обрабатывает подтверждение от контроллера
func (c *Client) handleAckMessage(envelope *protocol.Envelope) {
	var ack protocol.AckPayload
	if err := envelope.ParsePayload(&ack); err != nil {
		log.Printf("Failed to parse ack payload: %v", err)
		return
	}

	if ack.Success {
		log.Printf("Received successful ack for %s: %s", ack.MessageType, ack.Message)
	} else {
		log.Printf("Received failure ack for %s: %s", ack.MessageType, ack.Message)
	}
}

// handleErrorMessage - обрабатывает ошибку от контроллера
func (c *Client) handleErrorMessage(envelope *protocol.Envelope) {
	var errorPayload protocol.ErrorPayload
	if err := envelope.ParsePayload(&errorPayload); err != nil {
		log.Printf("Failed to parse error payload: %v", err)
		return
	}

	log.Printf("Received error from controller: [%d] %s - %s",
		errorPayload.Code, errorPayload.Message, errorPayload.Details)
}

// UpdateBalance - обновляет баланс и отправляет state_update контроллеру
func (c *Client) UpdateBalance(newBalance int) error {
	// Обновляем локальный ledger
	if err := c.ledger.UpdateBalance(c.config.ID, newBalance); err != nil {
		return fmt.Errorf("failed to update local balance: %w", err)
	}

	// Отправляем обновление контроллеру
	stateUpdate := protocol.StateUpdatePayload{
		Balance: newBalance,
		Version: c.ledger.GetVersion(),
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeStateUpdate, c.config.ID, stateUpdate)
	if err != nil {
		return fmt.Errorf("failed to create state update envelope: %w", err)
	}

	c.mutex.RLock()
	conn := c.conn
	connected := c.connected
	c.mutex.RUnlock()

	if !connected || conn == nil {
		return fmt.Errorf("not connected to controller")
	}

	if err := conn.WriteJSON(envelope); err != nil {
		return fmt.Errorf("failed to send state update: %w", err)
	}

	log.Printf("Sent state update: balance=%d, version=%d", newBalance, c.ledger.GetVersion())
	return nil
}

// GetBalance - возвращает текущий баланс
func (c *Client) GetBalance() int {
	return c.ledger.GetBalance(c.config.ID)
}

// GetVersion - возвращает текущую версию
func (c *Client) GetVersion() int {
	return c.ledger.GetVersion()
}

// IsConnected - проверяет, подключен ли кошелек к контроллеру
func (c *Client) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.connected
}
