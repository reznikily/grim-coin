package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	BroadcastPort = 9999
	BufferSize    = 65536
)

const (
	ControllerID  = "CONTROLLER"
	WebSocketPort = 8080
	DashboardPort = 8081
	InitialBalance = 100
	MaxRetries     = 5
)

// MessageType defines the type of network message
type MessageType string

const (
	MsgHello       MessageType = "HELLO"
	MsgHelloAck    MessageType = "HELLO_ACK"
	MsgTableSync   MessageType = "TABLE_SYNC"
	MsgTableAck    MessageType = "TABLE_ACK"
	MsgTransaction MessageType = "TRANSACTION"
	MsgTxAck       MessageType = "TX_ACK"
)

// Message represents a network message
type Message struct {
	Type      MessageType     `json:"type"`
	From      string          `json:"from"`
	FromIP    string          `json:"from_ip"`
	To        string          `json:"to,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp"`
	MessageID string          `json:"message_id"`
}

// WalletEntry represents a wallet in the network table
type WalletEntry struct {
	ID      string `json:"id"`
	IP      string `json:"ip"`
	Balance int    `json:"balance"`
}

// NetworkTable represents the state of all wallets
type NetworkTable struct {
	Wallets   map[string]*WalletEntry `json:"wallets"`
	Version   int64                   `json:"version"`
	Timestamp int64                   `json:"timestamp"`
}

// HelloData contains hello message payload
type HelloData struct {
	ID    string `json:"id"`
	IP    string `json:"ip"`
	IsNew bool   `json:"is_new"`
}

// TableSyncData contains table synchronization data
type TableSyncData struct {
	Table   NetworkTable `json:"table"`
	Version int64        `json:"version"`
}

// TransactionData contains transaction information
type TransactionData struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int    `json:"amount"`
	TxID   string `json:"tx_id"`
}

// NetworkManager handles network communication
type NetworkManager struct {
	localIP       string
	broadcastAddr string
	udpConn       *net.UDPConn
	handlers      map[MessageType]MessageHandler
	mu            sync.RWMutex
	ackChannels   map[string]chan bool
	ackMu         sync.RWMutex
}

type MessageHandler func(*Message) error

// Controller represents the network controller
type Controller struct {
	table          NetworkTable
	network        *NetworkManager
	mu             sync.RWMutex
	wsClients      map[*websocket.Conn]bool
	wsClientsMu    sync.RWMutex
	wsUpgrader     websocket.Upgrader
	localIP        string
	processedTxs   map[string]bool
	processedTxsMu sync.RWMutex
}

// NewMessage creates a new message
func NewMessage(msgType MessageType, from, fromIP string) *Message {
	return &Message{
		Type:      msgType,
		From:      from,
		FromIP:    fromIP,
		Timestamp: time.Now().Unix(),
		MessageID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), from),
	}
}

// NewNetworkManager creates a new network manager
func NewNetworkManager(localIP string) (*NetworkManager, error) {
	broadcastAddr := calculateBroadcastAddr(localIP)

	addr := &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: BroadcastPort,
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP: %v", err)
	}

	nm := &NetworkManager{
		localIP:       localIP,
		broadcastAddr: broadcastAddr,
		udpConn:       conn,
		handlers:      make(map[MessageType]MessageHandler),
		ackChannels:   make(map[string]chan bool),
	}

	return nm, nil
}

func (nm *NetworkManager) RegisterHandler(msgType MessageType, handler MessageHandler) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.handlers[msgType] = handler
}

func (nm *NetworkManager) Start() {
	go nm.listenLoop()
}

func (nm *NetworkManager) listenLoop() {
	buffer := make([]byte, BufferSize)

	for {
		n, remoteAddr, err := nm.udpConn.ReadFromUDP(buffer)
		if err != nil {
			continue
		}

		var msg Message
		if err := json.Unmarshal(buffer[:n], &msg); err != nil {
			continue
		}

		if msg.FromIP == nm.localIP {
			continue
		}

		if strings.HasSuffix(string(msg.Type), "_ACK") {
			nm.handleAck(msg.MessageID)
			continue
		}

		nm.mu.RLock()
		handler, ok := nm.handlers[msg.Type]
		nm.mu.RUnlock()

		if ok {
			go handler(&msg)
		}

		if nm.requiresAck(msg.Type) {
			// Use actual remote address from UDP packet
			nm.sendAck(&msg, remoteAddr.IP.String())
		}
	}
}

func (nm *NetworkManager) requiresAck(msgType MessageType) bool {
	switch msgType {
	case MsgHello, MsgTableSync, MsgTransaction:
		return true
	default:
		return false
	}
}

func (nm *NetworkManager) sendAck(original *Message, toIP string) {
	ackType := MessageType(string(original.Type) + "_ACK")
	ackMsg := &Message{
		Type:      ackType,
		From:      "",  // ACK doesn't need From
		FromIP:    nm.localIP,
		To:        original.From,
		MessageID: original.MessageID,
		Timestamp: time.Now().Unix(),
	}

	nm.SendTo(ackMsg, toIP)
	// Debug log
	fmt.Printf("[ACK] Sent %s ACK for message %s to %s\n", original.Type, original.MessageID[:16], toIP)
}

func (nm *NetworkManager) handleAck(messageID string) {
	nm.ackMu.RLock()
	ch, ok := nm.ackChannels[messageID]
	nm.ackMu.RUnlock()

	fmt.Printf("[ACK] Received ACK for message %s (registered: %v)\n", messageID[:16], ok)

	if ok {
		select {
		case ch <- true:
		default:
		}
	}
}

func (nm *NetworkManager) SendTo(msg *Message, ip string) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	addr := &net.UDPAddr{
		IP:   net.ParseIP(ip),
		Port: BroadcastPort,
	}

	_, err = nm.udpConn.WriteToUDP(data, addr)
	return err
}

func (nm *NetworkManager) SendWithRetry(msg *Message, targetIP string, maxRetries int) error {
	ackCh := make(chan bool, 1)
	nm.ackMu.Lock()
	nm.ackChannels[msg.MessageID] = ackCh
	nm.ackMu.Unlock()

	defer func() {
		nm.ackMu.Lock()
		delete(nm.ackChannels, msg.MessageID)
		nm.ackMu.Unlock()
		close(ackCh)
	}()

	for i := 0; i < maxRetries; i++ {
		nm.SendTo(msg, targetIP)

		select {
		case <-ackCh:
			return nil
		case <-time.After(2 * time.Second):
			if i < maxRetries-1 {
				fmt.Printf("Retry %d/%d for message %s\n", i+1, maxRetries, msg.MessageID)
			}
		}
	}

	return fmt.Errorf("failed to deliver message after %d retries", maxRetries)
}

func (nm *NetworkManager) BroadcastWithRetry(msg *Message, nodeIPs []string, maxRetries int) error {
	var wg sync.WaitGroup
	errors := make(chan error, len(nodeIPs))

	for _, ip := range nodeIPs {
		if ip == nm.localIP {
			continue
		}

		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			if err := nm.SendWithRetry(msg, targetIP, maxRetries); err != nil {
				errors <- fmt.Errorf("failed to send to %s: %v", targetIP, err)
			}
		}(ip)
	}

	wg.Wait()
	close(errors)

	var errMsgs []string
	for err := range errors {
		errMsgs = append(errMsgs, err.Error())
	}

	if len(errMsgs) > 0 {
		return fmt.Errorf("delivery errors: %s", strings.Join(errMsgs, "; "))
	}

	return nil
}

func calculateBroadcastAddr(localIP string) string {
	parts := strings.Split(localIP, ".")
	if len(parts) == 4 {
		return fmt.Sprintf("%s.%s.%s.255", parts[0], parts[1], parts[2])
	}
	return "255.255.255.255"
}

func GetNetworkInterfaces() ([]net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var result []net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		hasIPv4 := false
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				hasIPv4 = true
				break
			}
		}

		if hasIPv4 {
			result = append(result, iface)
		}
	}

	return result, nil
}

func GetInterfaceIPv4(iface net.Interface) (string, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String(), nil
		}
	}

	return "", fmt.Errorf("no IPv4 address found")
}

func NewController(localIP string) (*Controller, error) {
	c := &Controller{
		table: NetworkTable{
			Wallets:   make(map[string]*WalletEntry),
			Version:   1,
			Timestamp: time.Now().Unix(),
		},
		wsClients: make(map[*websocket.Conn]bool),
		wsUpgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		localIP:      localIP,
		processedTxs: make(map[string]bool),
	}

	nm, err := NewNetworkManager(localIP)
	if err != nil {
		return nil, fmt.Errorf("failed to create network manager: %v", err)
	}
	c.network = nm

	c.network.RegisterHandler(MsgHello, c.handleHello)
	c.network.RegisterHandler(MsgTransaction, c.handleTransaction)

	c.table.Wallets[ControllerID] = &WalletEntry{
		ID:      ControllerID,
		IP:      localIP,
		Balance: 0,
	}

	return c, nil
}

func (c *Controller) Start() error {
	c.network.Start()

	go c.startWebSocketServer()
	go c.startDashboardServer()

	fmt.Printf("Controller started on %s\n", c.localIP)
	fmt.Printf("WebSocket server: ws://%s:%d/ws\n", c.localIP, WebSocketPort)
	fmt.Printf("Dashboard: http://%s:%d/dashboard.html\n", c.localIP, DashboardPort)
	fmt.Println("Listening for wallets...")

	select {}
}

func (c *Controller) handleHello(msg *Message) error {
	var helloData HelloData
	if err := json.Unmarshal(msg.Data, &helloData); err != nil {
		return err
	}

	fmt.Printf("Received HELLO from %s (%s)\n", helloData.ID, helloData.IP)

	c.mu.Lock()
	isNew := false
	if _, exists := c.table.Wallets[helloData.ID]; !exists {
		c.table.Wallets[helloData.ID] = &WalletEntry{
			ID:      helloData.ID,
			IP:      helloData.IP,
			Balance: InitialBalance,
		}
		c.table.Version++
		c.table.Timestamp = time.Now().Unix()
		isNew = true
	}
	tableCopy := c.table
	c.mu.Unlock()

	responseData := HelloData{
		ID:    ControllerID,
		IP:    c.localIP,
		IsNew: false,
	}

	data, _ := json.Marshal(responseData)
	helloMsg := NewMessage(MsgHello, ControllerID, c.localIP)
	helloMsg.To = helloData.ID
	helloMsg.Data = data

	c.network.SendWithRetry(helloMsg, helloData.IP, MaxRetries)

	syncData := TableSyncData{
		Table:   tableCopy,
		Version: tableCopy.Version,
	}

	syncDataBytes, _ := json.Marshal(syncData)
	syncMsg := NewMessage(MsgTableSync, ControllerID, c.localIP)
	syncMsg.To = helloData.ID
	syncMsg.Data = syncDataBytes

	if err := c.network.SendWithRetry(syncMsg, helloData.IP, MaxRetries); err != nil {
		fmt.Printf("Failed to send table to %s: %v\n", helloData.ID, err)
	} else {
		fmt.Printf("Sent table (version %d) to %s\n", tableCopy.Version, helloData.ID)
	}

	if isNew {
		go c.broadcastTableToAll()
		c.broadcastToWebSockets()
	}

	return nil
}

func (c *Controller) handleTransaction(msg *Message) error {
	var txData TransactionData
	if err := json.Unmarshal(msg.Data, &txData); err != nil {
		return err
	}

	// Check if transaction already processed
	c.processedTxsMu.Lock()
	if c.processedTxs[txData.TxID] {
		c.processedTxsMu.Unlock()
		fmt.Printf("Skipping duplicate transaction: %s\n", txData.TxID[:8])
		return nil
	}
	c.processedTxs[txData.TxID] = true
	c.processedTxsMu.Unlock()

	fmt.Printf("Received transaction: %s -> %s: %d coins (TX: %s)\n",
		txData.From, txData.To, txData.Amount, txData.TxID[:8])

	c.mu.Lock()
	if fromWallet, ok := c.table.Wallets[txData.From]; ok {
		fromWallet.Balance -= txData.Amount
	}

	if toWallet, ok := c.table.Wallets[txData.To]; ok {
		toWallet.Balance += txData.Amount
	}

	c.table.Version++
	c.table.Timestamp = time.Now().Unix()
	c.mu.Unlock()

	c.broadcastToWebSockets()

	return nil
}

func (c *Controller) broadcastTableToAll() {
	c.mu.RLock()
	syncData := TableSyncData{
		Table:   c.table,
		Version: c.table.Version,
	}

	nodeIPs := make([]string, 0, len(c.table.Wallets))
	for _, wallet := range c.table.Wallets {
		if wallet.ID != ControllerID {
			nodeIPs = append(nodeIPs, wallet.IP)
		}
	}
	c.mu.RUnlock()

	syncDataBytes, _ := json.Marshal(syncData)
	syncMsg := NewMessage(MsgTableSync, ControllerID, c.localIP)
	syncMsg.Data = syncDataBytes

	c.network.BroadcastWithRetry(syncMsg, nodeIPs, MaxRetries)
}

func (c *Controller) startWebSocketServer() {
	http.HandleFunc("/ws", c.handleWebSocket)

	addr := fmt.Sprintf(":%d", WebSocketPort)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("WebSocket server error: %v\n", err)
	}
}

func (c *Controller) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := c.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("WebSocket upgrade error: %v\n", err)
		return
	}

	c.wsClientsMu.Lock()
	c.wsClients[conn] = true
	c.wsClientsMu.Unlock()

	fmt.Printf("New WebSocket client connected from %s\n", r.RemoteAddr)

	c.mu.RLock()
	tableData, _ := json.Marshal(c.table)
	c.mu.RUnlock()

	conn.WriteMessage(websocket.TextMessage, tableData)

	go func() {
		defer func() {
			c.wsClientsMu.Lock()
			delete(c.wsClients, conn)
			c.wsClientsMu.Unlock()
			conn.Close()
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

func (c *Controller) broadcastToWebSockets() {
	c.mu.RLock()
	tableData, _ := json.Marshal(c.table)
	c.mu.RUnlock()

	c.wsClientsMu.RLock()
	defer c.wsClientsMu.RUnlock()

	for client := range c.wsClients {
		err := client.WriteMessage(websocket.TextMessage, tableData)
		if err != nil {
			fmt.Printf("WebSocket write error: %v\n", err)
		}
	}
}

func (c *Controller) startDashboardServer() {
	http.HandleFunc("/dashboard.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(dashboardHTML))
	})

	addr := fmt.Sprintf(":%d", DashboardPort)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Dashboard server error: %v\n", err)
	}
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Grim Coin Network Dashboard</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); min-height: 100vh; padding: 20px; }
        .container { max-width: 1200px; margin: 0 auto; }
        .header { text-align: center; color: white; margin-bottom: 30px; }
        .header h1 { font-size: 3em; margin-bottom: 10px; text-shadow: 2px 2px 4px rgba(0,0,0,0.3); }
        .status { display: inline-block; padding: 8px 20px; background: rgba(255,255,255,0.2); border-radius: 20px; font-size: 0.9em; }
        .status.connected { background: rgba(76, 175, 80, 0.8); }
        .status.disconnected { background: rgba(244, 67, 54, 0.8); }
        .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 20px; margin-bottom: 30px; }
        .stat-card { background: white; border-radius: 15px; padding: 25px; box-shadow: 0 10px 30px rgba(0,0,0,0.2); transition: transform 0.3s; }
        .stat-card:hover { transform: translateY(-5px); }
        .stat-card h3 { color: #667eea; font-size: 0.9em; text-transform: uppercase; letter-spacing: 1px; margin-bottom: 10px; }
        .stat-card .value { font-size: 2.5em; font-weight: bold; color: #333; }
        .wallets-section { background: white; border-radius: 15px; padding: 30px; box-shadow: 0 10px 30px rgba(0,0,0,0.2); }
        .wallets-section h2 { color: #667eea; margin-bottom: 20px; font-size: 1.8em; }
        .wallet-grid { display: grid; gap: 15px; }
        .wallet-item { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); border-radius: 10px; padding: 20px; color: white; display: grid; grid-template-columns: 1fr auto auto; align-items: center; gap: 20px; transition: transform 0.3s; box-shadow: 0 5px 15px rgba(0,0,0,0.1); }
        .wallet-item:hover { transform: scale(1.02); }
        .wallet-item.controller { background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%); }
        .wallet-info h3 { font-size: 1.3em; margin-bottom: 5px; }
        .wallet-info .ip { opacity: 0.9; font-size: 0.9em; }
        .wallet-balance { text-align: right; }
        .wallet-balance .label { font-size: 0.8em; opacity: 0.8; }
        .wallet-balance .amount { font-size: 2em; font-weight: bold; }
        .wallet-icon { font-size: 2em; }
        .last-update { text-align: center; color: white; margin-top: 20px; opacity: 0.8; }
        .empty-state { text-align: center; padding: 60px 20px; color: #999; }
        .empty-state .icon { font-size: 4em; margin-bottom: 20px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header"><h1>💰 Grim Coin Network</h1><div class="status" id="status">Connecting...</div></div>
        <div class="stats">
            <div class="stat-card"><h3>Total Wallets</h3><div class="value" id="totalWallets">0</div></div>
            <div class="stat-card"><h3>Total Coins</h3><div class="value" id="totalCoins">0</div></div>
            <div class="stat-card"><h3>Network Version</h3><div class="value" id="networkVersion">0</div></div>
        </div>
        <div class="wallets-section">
            <h2>Active Wallets</h2>
            <div class="wallet-grid" id="walletGrid"><div class="empty-state"><div class="icon">🔍</div><p>Waiting for wallets to connect...</p></div></div>
        </div>
        <div class="last-update" id="lastUpdate">Last update: Never</div>
    </div>
    <script>
        let ws = null, reconnectTimeout = null;
        function connect() {
            const wsUrl = 'ws://' + window.location.hostname + ':8080/ws';
            ws = new WebSocket(wsUrl);
            ws.onopen = () => { console.log('WebSocket connected'); updateStatus('connected'); };
            ws.onmessage = (e) => { updateDashboard(JSON.parse(e.data)); };
            ws.onclose = () => { console.log('WebSocket disconnected'); updateStatus('disconnected'); reconnectTimeout = setTimeout(connect, 3000); };
            ws.onerror = (e) => { console.error('WebSocket error:', e); };
        }
        function updateStatus(status) {
            const statusEl = document.getElementById('status');
            statusEl.className = 'status ' + status;
            statusEl.textContent = status === 'connected' ? '🟢 Connected' : '🔴 Disconnected';
        }
        function updateDashboard(data) {
            const wallets = data.wallets || {};
            const walletCount = Object.keys(wallets).length;
            let totalCoins = 0;
            for (const id in wallets) totalCoins += wallets[id].balance || 0;
            document.getElementById('totalWallets').textContent = walletCount;
            document.getElementById('totalCoins').textContent = totalCoins.toLocaleString();
            document.getElementById('networkVersion').textContent = data.version || 0;
            const walletGrid = document.getElementById('walletGrid');
            if (walletCount === 0) {
                walletGrid.innerHTML = '<div class="empty-state"><div class="icon">🔍</div><p>Waiting for wallets to connect...</p></div>';
            } else {
                let html = '';
                const sortedWallets = Object.values(wallets).sort((a, b) => {
                    if (a.id === 'CONTROLLER') return -1;
                    if (b.id === 'CONTROLLER') return 1;
                    return b.balance - a.balance;
                });
                for (const wallet of sortedWallets) {
                    const isController = wallet.id === 'CONTROLLER';
                    const icon = isController ? '🎮' : '👛';
                    const className = isController ? 'wallet-item controller' : 'wallet-item';
                    html += '<div class="' + className + '"><div class="wallet-icon">' + icon + '</div><div class="wallet-info"><h3>' + wallet.id + '</h3><div class="ip">' + wallet.ip + '</div></div><div class="wallet-balance"><div class="label">Balance</div><div class="amount">' + wallet.balance + '</div></div></div>';
                }
                walletGrid.innerHTML = html;
            }
            document.getElementById('lastUpdate').textContent = 'Last update: ' + new Date().toLocaleTimeString();
        }
        connect();
        window.addEventListener('beforeunload', () => { if (reconnectTimeout) clearTimeout(reconnectTimeout); if (ws) ws.close(); });
    </script>
</body>
</html>`

func main() {
	reader := bufio.NewReader(os.Stdin)

	interfaces, err := GetNetworkInterfaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting interfaces: %v\n", err)
		os.Exit(1)
	}

	if len(interfaces) == 0 {
		fmt.Fprintf(os.Stderr, "No network interfaces found\n")
		os.Exit(1)
	}

	fmt.Println("Available network interfaces:")
	for i, iface := range interfaces {
		ip, _ := GetInterfaceIPv4(iface)
		fmt.Printf("%d. %s (%s)\n", i+1, iface.Name, ip)
	}

	fmt.Print("Select interface (number): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(interfaces) {
		fmt.Fprintf(os.Stderr, "Invalid interface selection\n")
		os.Exit(1)
	}

	selectedIface := interfaces[idx-1]
	ip, err := GetInterfaceIPv4(selectedIface)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get IP address: %v\n", err)
		os.Exit(1)
	}

	c, err := NewController(ip)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating controller: %v\n", err)
		os.Exit(1)
	}

	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting controller: %v\n", err)
		os.Exit(1)
	}
}
