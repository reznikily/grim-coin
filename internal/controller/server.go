package controller

import (
	"context"
	"fmt"
	"grim-coin/internal/discovery"
	"grim-coin/internal/protocol"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
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
	publicIP     string
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

// getPublicIP tries to determine the public IP address
func getPublicIP() (string, error) {
	// Try multiple services in case one is down
	services := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, service := range services {
		resp, err := client.Get(service)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		ip := strings.TrimSpace(string(body))
		// Validate it's a valid IP
		if net.ParseIP(ip) != nil {
			return ip, nil
		}
	}

	return "", fmt.Errorf("failed to get public IP from any service")
}
func (s *Server) Start(ctx context.Context, localIP string) error {
	// Get public IP address
	publicIP, err := getPublicIP()
	if err != nil {
		log.Printf("Warning: failed to get public IP, using local IP instead: %v", err)
		publicIP = localIP
	}
	s.publicIP = publicIP
	log.Printf("Public IP address: %s", publicIP)

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
		Addr:    "0.0.0.0:8000",
		Handler: walletMux,
	}
	observerMux := http.NewServeMux()
	observerMux.HandleFunc("/ws", s.handleObserverConnection)
	observerMux.HandleFunc("/", s.handleObserverHTML)
	observerServer := &http.Server{
		Addr:    "0.0.0.0:8080",
		Handler: observerMux,
	}
	go func() {
		log.Printf("Starting wallet server on 0.0.0.0:8000")
		if err := walletServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Wallet server error: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting observer server on 0.0.0.0:8080")
		log.Printf("Observer UI available at: http://%s:8080", publicIP)
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

func (s *Server) handleObserverHTML(w http.ResponseWriter, r *http.Request) {
	// Generate HTML with embedded public IP
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>GrimCoin - Наблюдатель сети</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Arial, sans-serif;
            margin: 0;
            padding: 20px;
            background-color: #f5f5f5;
            line-height: 1.6;
        }

        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 20px;
            border-radius: 10px;
            text-align: center;
            margin-bottom: 20px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }

        .header h1 {
            margin: 0 0 10px 0;
            font-size: 2.5em;
        }

        .status {
            padding: 10px 20px;
            border-radius: 25px;
            font-weight: bold;
            display: inline-block;
            margin-bottom: 20px;
        }

        .status.connected {
            background-color: #d4edda;
            color: #155724;
            border: 1px solid #c3e6cb;
        }

        .status.disconnected {
            background-color: #f8d7da;
            color: #721c24;
            border: 1px solid #f5c6cb;
        }

        .status.connecting {
            background-color: #fff3cd;
            color: #856404;
            border: 1px solid #ffeaa7;
        }

        .container {
            max-width: 1200px;
            margin: 0 auto;
        }

        .card {
            background: white;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0, 0, 0, 0.1);
            margin-bottom: 20px;
            overflow: hidden;
        }

        .card-header {
            background-color: #f8f9fa;
            padding: 15px 20px;
            border-bottom: 1px solid #e9ecef;
            font-weight: bold;
            font-size: 1.2em;
        }

        .card-body {
            padding: 20px;
        }

        table {
            width: 100%;
            border-collapse: collapse;
            margin-top: 10px;
        }

        th, td {
            text-align: left;
            padding: 12px;
            border-bottom: 1px solid #e9ecef;
        }

        th {
            background-color: #f8f9fa;
            font-weight: 600;
            color: #495057;
        }

        tr:hover {
            background-color: #f8f9fa;
        }

        .status-online {
            color: #28a745;
            font-weight: bold;
        }

        .status-offline {
            color: #dc3545;
            font-weight: bold;
        }

        .balance {
            font-weight: bold;
            color: #007bff;
        }

        .timestamp {
            color: #6c757d;
            font-size: 0.9em;
            margin-top: 10px;
        }

        .no-data {
            text-align: center;
            color: #6c757d;
            font-style: italic;
            padding: 40px;
        }

        .metrics {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 20px;
            margin-bottom: 20px;
        }

        .metric {
            background: white;
            padding: 20px;
            border-radius: 10px;
            text-align: center;
            box-shadow: 0 2px 5px rgba(0, 0, 0, 0.1);
        }

        .metric-value {
            font-size: 2em;
            font-weight: bold;
            color: #007bff;
            margin-bottom: 5px;
        }

        .metric-label {
            color: #6c757d;
            font-size: 0.9em;
        }

        .error-message {
            background-color: #f8d7da;
            color: #721c24;
            padding: 15px;
            border-radius: 5px;
            margin-bottom: 20px;
            border-left: 4px solid #dc3545;
        }

        @keyframes pulse {
            0% { opacity: 1; }
            50% { opacity: 0.5; }
            100% { opacity: 1; }
        }

        .connecting .status {
            animation: pulse 2s infinite;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🪙 GrimCoin</h1>
            <p>Наблюдатель децентрализованной платёжной сети</p>
            <div id="connection-status" class="status connecting">🔄 Подключение к контроллеру...</div>
        </div>

        <div id="error-container"></div>

        <div class="metrics">
            <div class="metric">
                <div id="total-nodes" class="metric-value">0</div>
                <div class="metric-label">Всего узлов</div>
            </div>
            <div class="metric">
                <div id="online-nodes" class="metric-value">0</div>
                <div class="metric-label">Онлайн</div>
            </div>
            <div class="metric">
                <div id="total-balance" class="metric-value">0</div>
                <div class="metric-label">Общий баланс</div>
            </div>
            <div class="metric">
                <div id="network-version" class="metric-value">0</div>
                <div class="metric-label">Версия сети</div>
            </div>
        </div>

        <div class="card">
            <div class="card-header">
                📊 Состояние узлов сети
            </div>
            <div class="card-body">
                <table id="nodes-table">
                    <thead>
                        <tr>
                            <th>ID</th>
                            <th>Имя</th>
                            <th>IP адрес</th>
                            <th>Баланс</th>
                            <th>Версия</th>
                            <th>Статус</th>
                        </tr>
                    </thead>
                    <tbody id="nodes-tbody">
                        <tr>
                            <td colspan="6" class="no-data">Ожидание данных от контроллера...</td>
                        </tr>
                    </tbody>
                </table>
                <div id="last-update" class="timestamp"></div>
            </div>
        </div>
    </div>

    <script>
        class GrimCoinObserver {
            constructor() {
                this.socket = null;
                this.reconnectAttempts = 0;
                this.maxReconnectAttempts = 5;
                this.reconnectInterval = 3000;
                this.nodes = [];
                
                this.initElements();
                this.connect();
            }

            initElements() {
                this.statusElement = document.getElementById('connection-status');
                this.errorContainer = document.getElementById('error-container');
                this.nodesTableBody = document.getElementById('nodes-tbody');
                this.lastUpdateElement = document.getElementById('last-update');
                
                this.totalNodesElement = document.getElementById('total-nodes');
                this.onlineNodesElement = document.getElementById('online-nodes');
                this.totalBalanceElement = document.getElementById('total-balance');
                this.networkVersionElement = document.getElementById('network-version');
            }

            connect() {
                try {
                    // Connect to the controller's public IP
                    const wsUrl = 'ws://%s:8080/ws';
                    console.log('Connecting to:', wsUrl);
                    this.socket = new WebSocket(wsUrl);
                    
                    this.socket.onopen = (event) => {
                        console.log('Connected to controller');
                        this.setStatus('connected', '✅ Подключен к контроллеру');
                        this.reconnectAttempts = 0;
                        this.clearError();
                    };
                    
                    this.socket.onmessage = (event) => {
                        try {
                            const envelope = JSON.parse(event.data);
                            this.handleMessage(envelope);
                        } catch (error) {
                            console.error('Failed to parse message:', error);
                            this.showError('Ошибка парсинга сообщения: ' + error.message);
                        }
                    };
                    
                    this.socket.onclose = (event) => {
                        console.log('Connection closed:', event);
                        this.setStatus('disconnected', '❌ Соединение разорвано');
                        this.scheduleReconnect();
                    };
                    
                    this.socket.onerror = (error) => {
                        console.error('WebSocket error:', error);
                        this.showError('Ошибка WebSocket соединения');
                    };
                    
                } catch (error) {
                    console.error('Failed to connect:', error);
                    this.setStatus('disconnected', '❌ Ошибка подключения');
                    this.scheduleReconnect();
                }
            }

            scheduleReconnect() {
                if (this.reconnectAttempts < this.maxReconnectAttempts) {
                    this.reconnectAttempts++;
                    this.setStatus('connecting', ` + "`🔄 Переподключение... (${this.reconnectAttempts}/${this.maxReconnectAttempts})`" + `);
                    
                    setTimeout(() => {
                        this.connect();
                    }, this.reconnectInterval);
                } else {
                    this.setStatus('disconnected', '❌ Не удалось подключиться к контроллеру');
                    this.showError('Превышено максимальное количество попыток переподключения. Обновите страницу для повтора.');
                }
            }

            handleMessage(envelope) {
                console.log('Received message:', envelope);
                
                if (envelope.type === 'network_state') {
                    this.updateNetworkState(envelope.payload);
                }
            }

            updateNetworkState(payload) {
                this.nodes = payload.nodes || [];
                this.renderNodesTable();
                this.updateMetrics();
                
                const timestamp = new Date(payload.timestamp);
                this.lastUpdateElement.textContent = ` + "`Последнее обновление: ${timestamp.toLocaleString('ru-RU')}`" + `;
            }

            renderNodesTable() {
                if (this.nodes.length === 0) {
                    this.nodesTableBody.innerHTML = ` + "`" + `
                        <tr>
                            <td colspan="6" class="no-data">Нет подключенных узлов</td>
                        </tr>
                    ` + "`" + `;
                    return;
                }

                // Сортируем узлы по ID
                const sortedNodes = [...this.nodes].sort((a, b) => a.id - b.id);

                this.nodesTableBody.innerHTML = sortedNodes.map(node => ` + "`" + `
                    <tr>
                        <td>${node.id}</td>
                        <td>${this.escapeHtml(node.name)}</td>
                        <td>${this.escapeHtml(node.ip)}</td>
                        <td class="balance">${node.balance} 🪙</td>
                        <td>${node.version}</td>
                        <td class="status-${node.status}">${node.status === 'online' ? 'Онлайн' : 'Офлайн'}</td>
                    </tr>
                ` + "`" + `).join('');
            }

            updateMetrics() {
                const totalNodes = this.nodes.length;
                const onlineNodes = this.nodes.filter(node => node.status === 'online').length;
                const totalBalance = this.nodes.reduce((sum, node) => sum + node.balance, 0);
                const maxVersion = Math.max(...this.nodes.map(node => node.version), 0);

                this.totalNodesElement.textContent = totalNodes;
                this.onlineNodesElement.textContent = onlineNodes;
                this.totalBalanceElement.textContent = totalBalance;
                this.networkVersionElement.textContent = maxVersion;
            }

            setStatus(type, message) {
                this.statusElement.className = ` + "`status ${type}`" + `;
                this.statusElement.textContent = message;
            }

            showError(message) {
                this.errorContainer.innerHTML = ` + "`" + `
                    <div class="error-message">
                        <strong>Ошибка:</strong> ${this.escapeHtml(message)}
                    </div>
                ` + "`" + `;
            }

            clearError() {
                this.errorContainer.innerHTML = '';
            }

            escapeHtml(text) {
                const div = document.createElement('div');
                div.textContent = text;
                return div.innerHTML;
            }
        }

        // Запускаем наблюдатель при загрузке страницы
        document.addEventListener('DOMContentLoaded', () => {
            new GrimCoinObserver();
        });
    </script>
</body>
</html>`, s.publicIP)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
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
