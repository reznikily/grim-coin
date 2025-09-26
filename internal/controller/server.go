package controller

import (
	"context"
	"grim-coin/internal/protocol"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WalletInfo - информация о кошельке в контроллере
type WalletInfo struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	IP       string          `json:"ip"`
	Balance  int             `json:"balance"`
	Version  int             `json:"version"`
	Status   string          `json:"status"`
	Conn     *websocket.Conn `json:"-"`
	LastSeen time.Time       `json:"-"`
}

// Server - основной сервер контроллера
type Server struct {
	wallets   map[int]*WalletInfo // ID -> информация о кошельке
	observers []*websocket.Conn   // соединения веб-клиентов
	mutex     sync.RWMutex
	upgrader  websocket.Upgrader
}

// NewServer - создает новый сервер контроллера
func NewServer() *Server {
	return &Server{
		wallets:   make(map[int]*WalletInfo),
		observers: make([]*websocket.Conn, 0),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // разрешаем все источники для локальной разработки
			},
		},
	}
}

// Start - запускает сервер на портах P1 (8000) и P2 (8080)
func (s *Server) Start(ctx context.Context) error {
	// P1: сервер для кошельков
	walletMux := http.NewServeMux()
	walletMux.HandleFunc("/ws", s.handleWalletConnection)
	walletServer := &http.Server{
		Addr:    ":8000",
		Handler: walletMux,
	}

	// P2: сервер для наблюдателей
	observerMux := http.NewServeMux()
	observerMux.HandleFunc("/ws", s.handleObserverConnection)
	observerServer := &http.Server{
		Addr:    ":8080",
		Handler: observerMux,
	}

	// Запускаем серверы в горутинах
	go func() {
		log.Printf("Starting wallet server on :8000")
		if err := walletServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Wallet server error: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting observer server on :8080")
		if err := observerServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Observer server error: %v", err)
		}
	}()

	// Ожидаем сигнал завершения
	<-ctx.Done()

	// Graceful shutdown
	log.Println("Shutting down servers...")
	walletServer.Shutdown(context.Background())
	observerServer.Shutdown(context.Background())

	return nil
}

// handleWalletConnection - обрабатывает подключения кошельков (P1)
func (s *Server) handleWalletConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("New wallet connection from %s", conn.RemoteAddr())

	for {
		var envelope protocol.Envelope
		if err := conn.ReadJSON(&envelope); err != nil {
			log.Printf("Failed to read message: %v", err)
			break
		}

		log.Printf("Received message type: %s from wallet: %d", envelope.Type, envelope.SenderID)

		switch envelope.Type {
		case protocol.MsgTypeHello:
			s.handleHelloMessage(&envelope, conn)
		case protocol.MsgTypeStateUpdate:
			s.handleStateUpdateMessage(&envelope)
		default:
			log.Printf("Unknown message type: %s", envelope.Type)
		}
	}

	// Помечаем кошелек как offline при отключении
	s.markWalletOffline(conn)
}

// handleObserverConnection - обрабатывает подключения наблюдателей (P2)
func (s *Server) handleObserverConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade observer connection: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("New observer connection from %s", conn.RemoteAddr())

	// Добавляем соединение в список наблюдателей
	s.addObserver(conn)
	defer s.removeObserver(conn)

	// Отправляем текущее состояние сразу после подключения
	s.broadcastNetworkState()

	// Держим соединение живым
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Observer disconnected: %v", err)
			break
		}
	}
}

// handleHelloMessage - обрабатывает hello сообщение от кошелька
func (s *Server) handleHelloMessage(envelope *protocol.Envelope, conn *websocket.Conn) {
	var hello protocol.HelloPayload
	if err := envelope.ParsePayload(&hello); err != nil {
		log.Printf("Failed to parse hello payload: %v", err)
		s.sendError(conn, envelope.SenderID, "invalid_hello_payload", err.Error())
		return
	}

	// Обновляем информацию о кошельке
	s.mutex.Lock()
	s.wallets[hello.ID] = &WalletInfo{
		ID:       hello.ID,
		Name:     hello.Name,
		IP:       hello.IP,
		Balance:  hello.Balance,
		Version:  hello.Version,
		Status:   "online",
		Conn:     conn,
		LastSeen: time.Now(),
	}
	s.mutex.Unlock()

	log.Printf("Wallet %d (%s) connected with balance %d, version %d",
		hello.ID, hello.Name, hello.Balance, hello.Version)

	// Отправляем подтверждение
	s.sendAck(conn, envelope.SenderID, protocol.MsgTypeHello, true, "welcome")

	// Уведомляем наблюдателей об изменении состояния сети
	s.broadcastNetworkState()
}

// handleStateUpdateMessage - обрабатывает обновление состояния от кошелька
func (s *Server) handleStateUpdateMessage(envelope *protocol.Envelope) {
	var stateUpdate protocol.StateUpdatePayload
	if err := envelope.ParsePayload(&stateUpdate); err != nil {
		log.Printf("Failed to parse state update payload: %v", err)
		return
	}

	// Обновляем информацию о кошельке
	s.mutex.Lock()
	if wallet, exists := s.wallets[envelope.SenderID]; exists {
		wallet.Balance = stateUpdate.Balance
		wallet.Version = stateUpdate.Version
		wallet.LastSeen = time.Now()
		log.Printf("Updated wallet %d: balance=%d, version=%d",
			envelope.SenderID, stateUpdate.Balance, stateUpdate.Version)
	}
	s.mutex.Unlock()

	// Уведомляем наблюдателей
	s.broadcastNetworkState()
}

// addObserver - добавляет наблюдателя в список
func (s *Server) addObserver(conn *websocket.Conn) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.observers = append(s.observers, conn)
}

// removeObserver - удаляет наблюдателя из списка
func (s *Server) removeObserver(conn *websocket.Conn) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for i, observer := range s.observers {
		if observer == conn {
			s.observers = append(s.observers[:i], s.observers[i+1:]...)
			break
		}
	}
}

// markWalletOffline - помечает кошелек как offline
func (s *Server) markWalletOffline(conn *websocket.Conn) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, wallet := range s.wallets {
		if wallet.Conn == conn {
			wallet.Status = "offline"
			wallet.Conn = nil
			log.Printf("Wallet %d (%s) went offline", wallet.ID, wallet.Name)
			break
		}
	}

	// Уведомляем наблюдателей
	go s.broadcastNetworkState()
}

// broadcastNetworkState - отправляет состояние сети всем наблюдателям
func (s *Server) broadcastNetworkState() {
	s.mutex.RLock()

	// Создаем список узлов
	nodes := make([]protocol.NodeInfo, 0, len(s.wallets))
	for _, wallet := range s.wallets {
		nodes = append(nodes, protocol.NodeInfo{
			ID:      wallet.ID,
			Name:    wallet.Name,
			IP:      wallet.IP,
			Balance: wallet.Balance,
			Version: wallet.Version,
			Status:  wallet.Status,
		})
	}

	// Создаем копию списка наблюдателей
	observersCopy := make([]*websocket.Conn, len(s.observers))
	copy(observersCopy, s.observers)

	s.mutex.RUnlock()

	networkState := protocol.NetworkStatePayload{
		Nodes:     nodes,
		Timestamp: time.Now(),
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeNetworkState, 0, networkState)
	if err != nil {
		log.Printf("Failed to create network state envelope: %v", err)
		return
	}

	// Отправляем всем наблюдателям
	for _, observer := range observersCopy {
		if err := observer.WriteJSON(envelope); err != nil {
			log.Printf("Failed to send network state to observer: %v", err)
		}
	}
}

// sendAck - отправляет подтверждение кошельку
func (s *Server) sendAck(conn *websocket.Conn, walletID int, messageType string, success bool, message string) {
	ack := protocol.AckPayload{
		MessageType: messageType,
		Success:     success,
		Message:     message,
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeAck, 0, ack)
	if err != nil {
		log.Printf("Failed to create ack envelope: %v", err)
		return
	}

	if err := conn.WriteJSON(envelope); err != nil {
		log.Printf("Failed to send ack to wallet %d: %v", walletID, err)
	}
}

// sendError - отправляет сообщение об ошибке кошельку
func (s *Server) sendError(conn *websocket.Conn, walletID int, code string, message string) {
	errorPayload := protocol.ErrorPayload{
		Code:    400,
		Message: code,
		Details: message,
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeError, 0, errorPayload)
	if err != nil {
		log.Printf("Failed to create error envelope: %v", err)
		return
	}

	if err := conn.WriteJSON(envelope); err != nil {
		log.Printf("Failed to send error to wallet %d: %v", walletID, err)
	}
}
