package controller

import (
	"context"
	"grim-coin/internal/discovery"
	"grim-coin/internal/protocol"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)
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

type LockState struct {
	TxID      string
	OwnerID   int
	ExpiresAt time.Time
}

type Server struct {
	wallets      map[int]*WalletInfo
	observers    []*websocket.Conn
	mutex        sync.RWMutex
	upgrader     websocket.Upgrader
	lockState    *LockState
	lockMutex    sync.Mutex
	statePushMap map[string]map[int]*protocol.StatePushResponsePayload
}

func NewServer() *Server {
	return &Server{
		wallets:      make(map[int]*WalletInfo),
		observers:    make([]*websocket.Conn, 0),
		statePushMap: make(map[string]map[int]*protocol.StatePushResponsePayload),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}
func (s *Server) Start(ctx context.Context, localIP string) error {
	disc, err := discovery.NewServiceWithIP(localIP)
	if err != nil {
		return err
	}
	defer disc.Close()

	go disc.StartAnnouncing(&discovery.Announcement{
		Type:   discovery.ServiceController,
		IP:     localIP,
		PortP1: 8000,
		PortP2: 8080,
	}, 3*time.Second)

	walletMux := http.NewServeMux()
	walletMux.HandleFunc("/ws", s.handleWalletConnection)
	walletServer := &http.Server{
		Addr:    ":8000",
		Handler: walletMux,
	}
	observerMux := http.NewServeMux()
	observerMux.HandleFunc("/ws", s.handleObserverConnection)
	observerServer := &http.Server{
		Addr:    ":8080",
		Handler: observerMux,
	}
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
	<-ctx.Done()
	log.Println("Shutting down servers...")
	walletServer.Shutdown(context.Background())
	observerServer.Shutdown(context.Background())

	return nil
}
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
		case protocol.MsgTypeLockRequest:
			s.handleLockRequest(&envelope, conn)
		case protocol.MsgTypeCommitNotice:
			s.handleCommitNotice(&envelope)
		case protocol.MsgTypeStatePushResponse:
			s.handleStatePushResponse(&envelope)
		default:
			log.Printf("Unknown message type: %s", envelope.Type)
		}
	}
	s.markWalletOffline(conn)
}
func (s *Server) handleObserverConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade observer connection: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("New observer connection from %s", conn.RemoteAddr())
	s.addObserver(conn)
	defer s.removeObserver(conn)
	s.broadcastNetworkState()
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Observer disconnected: %v", err)
			break
		}
	}
}
func (s *Server) handleHelloMessage(envelope *protocol.Envelope, conn *websocket.Conn) {
	var hello protocol.HelloPayload
	if err := envelope.ParsePayload(&hello); err != nil {
		log.Printf("Failed to parse hello payload: %v", err)
		s.sendError(conn, envelope.SenderID, "invalid_hello_payload", err.Error())
		return
	}
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
	s.sendAck(conn, envelope.SenderID, protocol.MsgTypeHello, true, "welcome")
	s.broadcastNetworkState()
}
func (s *Server) handleStateUpdateMessage(envelope *protocol.Envelope) {
	var stateUpdate protocol.StateUpdatePayload
	if err := envelope.ParsePayload(&stateUpdate); err != nil {
		log.Printf("Failed to parse state update payload: %v", err)
		return
	}
	s.mutex.Lock()
	if wallet, exists := s.wallets[envelope.SenderID]; exists {
		wallet.Balance = stateUpdate.Balance
		wallet.Version = stateUpdate.Version
		wallet.LastSeen = time.Now()
		log.Printf("Updated wallet %d: balance=%d, version=%d",
			envelope.SenderID, stateUpdate.Balance, stateUpdate.Version)
	}
	s.mutex.Unlock()
	s.broadcastNetworkState()
}
func (s *Server) addObserver(conn *websocket.Conn) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.observers = append(s.observers, conn)
}
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
	go s.broadcastNetworkState()
}
func (s *Server) broadcastNetworkState() {
	s.mutex.RLock()
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
	for _, observer := range observersCopy {
		if err := observer.WriteJSON(envelope); err != nil {
			log.Printf("Failed to send network state to observer: %v", err)
		}
	}
}
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

func (s *Server) handleLockRequest(envelope *protocol.Envelope, conn *websocket.Conn) {
	var req protocol.LockRequestPayload
	if err := envelope.ParsePayload(&req); err != nil {
		log.Printf("Failed to parse lock request: %v", err)
		return
	}

	s.lockMutex.Lock()
	defer s.lockMutex.Unlock()

	if s.lockState != nil && time.Now().Before(s.lockState.ExpiresAt) {
		s.sendError(conn, envelope.SenderID, "lock_busy", "Another transaction is in progress")
		return
	}

	ttl := 30000
	s.lockState = &LockState{
		TxID:      req.TxID,
		OwnerID:   envelope.SenderID,
		ExpiresAt: time.Now().Add(time.Duration(ttl) * time.Millisecond),
	}

	payload := protocol.LockGrantedPayload{
		TxID:  req.TxID,
		TTLMs: ttl,
	}

	resp, err := protocol.CreateEnvelope(protocol.MsgTypeLockGranted, 0, payload)
	if err != nil {
		log.Printf("Failed to create lock granted envelope: %v", err)
		return
	}

	if err := conn.WriteJSON(resp); err != nil {
		log.Printf("Failed to send lock granted: %v", err)
	}

	log.Printf("Lock granted to wallet %d for tx %s", envelope.SenderID, req.TxID)
}

func (s *Server) handleCommitNotice(envelope *protocol.Envelope) {
	var commit protocol.CommitNoticePayload
	if err := envelope.ParsePayload(&commit); err != nil {
		log.Printf("Failed to parse commit notice: %v", err)
		return
	}

	s.lockMutex.Lock()
	s.lockState = nil
	s.statePushMap[commit.TxID] = make(map[int]*protocol.StatePushResponsePayload)
	s.lockMutex.Unlock()

	log.Printf("Transaction %s committed, collecting states", commit.TxID)

	s.mutex.RLock()
	wallets := make([]*WalletInfo, 0, len(s.wallets))
	for _, w := range s.wallets {
		if w.Status == "online" && w.Conn != nil {
			wallets = append(wallets, w)
		}
	}
	s.mutex.RUnlock()

	req := protocol.StatePushRequestPayload{TxID: commit.TxID}
	for _, w := range wallets {
		env, _ := protocol.CreateEnvelope(protocol.MsgTypeStatePushRequest, 0, req)
		if err := w.Conn.WriteJSON(env); err != nil {
			log.Printf("Failed to send state push request to wallet %d: %v", w.ID, err)
		}
	}

	go s.waitAndVerifyStates(commit.TxID, len(wallets))
}

func (s *Server) handleStatePushResponse(envelope *protocol.Envelope) {
	var resp protocol.StatePushResponsePayload
	if err := envelope.ParsePayload(&resp); err != nil {
		log.Printf("Failed to parse state push response: %v", err)
		return
	}

	s.lockMutex.Lock()
	if states, ok := s.statePushMap[envelope.TxID]; ok {
		states[envelope.SenderID] = &resp
	}
	s.lockMutex.Unlock()
}

func (s *Server) waitAndVerifyStates(txID string, expectedCount int) {
	time.Sleep(2 * time.Second)

	s.lockMutex.Lock()
	states := s.statePushMap[txID]
	delete(s.statePushMap, txID)
	s.lockMutex.Unlock()

	if len(states) == 0 {
		return
	}

	var firstVersion int
	totalSum := 0
	consistent := true

	for _, state := range states {
		if firstVersion == 0 {
			firstVersion = state.Version
		}
		if state.Version != firstVersion {
			consistent = false
		}
		for _, bal := range state.Balances {
			totalSum += bal
		}
	}

	if !consistent {
		log.Printf("Warning: version mismatch detected for tx %s", txID)
	}

	log.Printf("State verification for tx %s: collected %d/%d states, sum=%d, version=%d",
		txID, len(states), expectedCount, totalSum, firstVersion)

	s.mutex.Lock()
	for id, state := range states {
		if wallet, ok := s.wallets[id]; ok {
			if bal, exists := state.Balances[id]; exists {
				wallet.Balance = bal
			}
			wallet.Version = state.Version
		}
	}
	s.mutex.Unlock()

	s.broadcastNetworkState()
}
