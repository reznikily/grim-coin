package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	BroadcastPort = 9999
	BufferSize    = 65536
)

const (
	ProfileFile    = ".wallet_profile.json"
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
	MsgLockRequest MessageType = "LOCK_REQUEST"
	MsgLockGrant   MessageType = "LOCK_GRANT"
	MsgLockRelease MessageType = "LOCK_RELEASE"
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

// LockRequestData contains lock request information
type LockRequestData struct {
	RequestID string `json:"request_id"`
	NodeID    string `json:"node_id"`
	Timestamp int64  `json:"timestamp"`
}

// Profile stores local wallet configuration
type Profile struct {
	ID        string `json:"id"`
	Interface string `json:"interface"`
	IP        string `json:"ip"`
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

// Wallet represents a wallet node in the network
type Wallet struct {
	profile         Profile
	table           NetworkTable
	network         *NetworkManager
	mu              sync.RWMutex
	isLocked        bool
	lockHolder      string
	pendingLocks    map[string]chan bool
	lockMu          sync.RWMutex
	processedTxs    map[string]bool
	processedTxsMu  sync.RWMutex
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

// RegisterHandler registers a message handler
func (nm *NetworkManager) RegisterHandler(msgType MessageType, handler MessageHandler) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.handlers[msgType] = handler
}

// Start starts listening for messages
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

// Broadcast sends a message to all nodes in the network
func (nm *NetworkManager) Broadcast(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	addr := &net.UDPAddr{
		IP:   net.ParseIP(nm.broadcastAddr),
		Port: BroadcastPort,
	}

	_, err = nm.udpConn.WriteToUDP(data, addr)
	return err
}

// SendTo sends a message to a specific IP
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

// SendWithRetry sends a message with guaranteed delivery
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
		if targetIP == "" {
			nm.Broadcast(msg)
		} else {
			nm.SendTo(msg, targetIP)
		}

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

// BroadcastWithRetry broadcasts with guaranteed delivery to all nodes
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

// Close closes the network manager
func (nm *NetworkManager) Close() {
	if nm.udpConn != nil {
		nm.udpConn.Close()
	}
}

func calculateBroadcastAddr(localIP string) string {
	parts := strings.Split(localIP, ".")
	if len(parts) == 4 {
		return fmt.Sprintf("%s.%s.%s.255", parts[0], parts[1], parts[2])
	}
	return "255.255.255.255"
}

// GetNetworkInterfaces returns all available network interfaces
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

// GetInterfaceIPv4 gets the IPv4 address of an interface
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

// NewWallet creates a new wallet
func NewWallet() (*Wallet, error) {
	w := &Wallet{
		table: NetworkTable{
			Wallets: make(map[string]*WalletEntry),
			Version: 0,
		},
		pendingLocks: make(map[string]chan bool),
		processedTxs: make(map[string]bool),
	}

	if _, err := os.Stat(ProfileFile); err == nil {
		data, err := os.ReadFile(ProfileFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read profile: %v", err)
		}

		if err := json.Unmarshal(data, &w.profile); err != nil {
			return nil, fmt.Errorf("failed to parse profile: %v", err)
		}

		fmt.Printf("Loaded existing profile: ID=%s, IP=%s\n", w.profile.ID, w.profile.IP)
	} else {
		if err := w.setupNewProfile(); err != nil {
			return nil, err
		}
	}

	nm, err := NewNetworkManager(w.profile.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to create network manager: %v", err)
	}
	w.network = nm

	w.network.RegisterHandler(MsgHello, w.handleHello)
	w.network.RegisterHandler(MsgTableSync, w.handleTableSync)
	w.network.RegisterHandler(MsgTransaction, w.handleTransaction)
	w.network.RegisterHandler(MsgLockRequest, w.handleLockRequest)
	w.network.RegisterHandler(MsgLockGrant, w.handleLockGrant)
	w.network.RegisterHandler(MsgLockRelease, w.handleLockRelease)

	return w, nil
}

func (w *Wallet) setupNewProfile() error {
	reader := bufio.NewReader(os.Stdin)

	interfaces, err := GetNetworkInterfaces()
	if err != nil {
		return fmt.Errorf("failed to get network interfaces: %v", err)
	}

	if len(interfaces) == 0 {
		return fmt.Errorf("no network interfaces found")
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
		return fmt.Errorf("invalid interface selection")
	}

	selectedIface := interfaces[idx-1]
	ip, err := GetInterfaceIPv4(selectedIface)
	if err != nil {
		return fmt.Errorf("failed to get IP address: %v", err)
	}

	fmt.Print("Enter wallet ID (name): ")
	id, _ := reader.ReadString('\n')
	id = strings.TrimSpace(id)

	if id == "" {
		return fmt.Errorf("wallet ID cannot be empty")
	}

	w.profile = Profile{
		ID:        id,
		Interface: selectedIface.Name,
		IP:        ip,
	}

	data, err := json.MarshalIndent(w.profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %v", err)
	}

	if err := os.WriteFile(ProfileFile, data, 0644); err != nil {
		return fmt.Errorf("failed to save profile: %v", err)
	}

	fmt.Printf("Profile saved: ID=%s, IP=%s\n", w.profile.ID, w.profile.IP)
	return nil
}

// Start starts the wallet
func (w *Wallet) Start() error {
	w.network.Start()

	fmt.Println("Sending HELLO to network...")
	if err := w.sendHello(); err != nil {
		return fmt.Errorf("failed to send hello: %v", err)
	}

	time.Sleep(3 * time.Second)
	w.runCLI()

	return nil
}

func (w *Wallet) sendHello() error {
	helloData := HelloData{
		ID:    w.profile.ID,
		IP:    w.profile.IP,
		IsNew: true,
	}

	data, _ := json.Marshal(helloData)
	msg := NewMessage(MsgHello, w.profile.ID, w.profile.IP)
	msg.Data = data

	return w.network.Broadcast(msg)
}

func (w *Wallet) handleHello(msg *Message) error {
	var helloData HelloData
	if err := json.Unmarshal(msg.Data, &helloData); err != nil {
		return err
	}

	fmt.Printf("Received HELLO from %s (%s)\n", helloData.ID, helloData.IP)

	w.mu.Lock()
	if _, exists := w.table.Wallets[helloData.ID]; !exists {
		w.table.Wallets[helloData.ID] = &WalletEntry{
			ID:      helloData.ID,
			IP:      helloData.IP,
			Balance: InitialBalance,
		}
	}
	w.mu.Unlock()

	return nil
}

func (w *Wallet) handleTableSync(msg *Message) error {
	var syncData TableSyncData
	if err := json.Unmarshal(msg.Data, &syncData); err != nil {
		return err
	}

	fmt.Printf("Received table sync (version %d) from %s\n", syncData.Version, msg.From)

	w.mu.Lock()
	if syncData.Version > w.table.Version {
		w.table = syncData.Table
		fmt.Printf("Table updated to version %d\n", w.table.Version)
	}
	w.mu.Unlock()

	return nil
}

func (w *Wallet) handleTransaction(msg *Message) error {
	var txData TransactionData
	if err := json.Unmarshal(msg.Data, &txData); err != nil {
		return err
	}

	// Check if transaction already processed
	w.processedTxsMu.Lock()
	if w.processedTxs[txData.TxID] {
		w.processedTxsMu.Unlock()
		fmt.Printf("Skipping duplicate transaction: %s\n", txData.TxID[:8])
		return nil
	}
	w.processedTxs[txData.TxID] = true
	w.processedTxsMu.Unlock()

	fmt.Printf("Received transaction: %s -> %s: %d coins (TX: %s)\n",
		txData.From, txData.To, txData.Amount, txData.TxID[:8])

	w.mu.Lock()
	defer w.mu.Unlock()

	if fromWallet, ok := w.table.Wallets[txData.From]; ok {
		fromWallet.Balance -= txData.Amount
	}

	if toWallet, ok := w.table.Wallets[txData.To]; ok {
		toWallet.Balance += txData.Amount
	}

	w.table.Version++
	w.table.Timestamp = time.Now().Unix()

	return nil
}

func (w *Wallet) handleLockRequest(msg *Message) error {
	var lockData LockRequestData
	if err := json.Unmarshal(msg.Data, &lockData); err != nil {
		return err
	}

	w.lockMu.Lock()
	canGrant := !w.isLocked
	if canGrant {
		w.isLocked = true
		w.lockHolder = lockData.NodeID
	}
	w.lockMu.Unlock()

	if canGrant {
		grantMsg := NewMessage(MsgLockGrant, w.profile.ID, w.profile.IP)
		grantMsg.To = lockData.NodeID
		grantData, _ := json.Marshal(lockData)
		grantMsg.Data = grantData

		// Send grant with retries to ensure delivery
		go func() {
			for i := 0; i < 5; i++ {
				w.network.SendTo(grantMsg, msg.FromIP)
				time.Sleep(200 * time.Millisecond)
			}
		}()
		
		fmt.Printf("Lock granted to %s (request: %s)\n", lockData.NodeID, lockData.RequestID[:8])
	} else {
		fmt.Printf("Lock denied to %s (held by %s)\n", lockData.NodeID, w.lockHolder)
	}

	return nil
}

func (w *Wallet) handleLockGrant(msg *Message) error {
	var lockData LockRequestData
	if err := json.Unmarshal(msg.Data, &lockData); err != nil {
		return err
	}

	fmt.Printf("Received lock grant from %s (request: %s)\n", msg.From, lockData.RequestID[:8])

	w.lockMu.RLock()
	ch, ok := w.pendingLocks[lockData.RequestID]
	w.lockMu.RUnlock()

	if ok {
		select {
		case ch <- true:
		default:
			// Channel full or closed, ignore
		}
	} else {
		fmt.Printf("Warning: received grant for unknown request %s\n", lockData.RequestID[:8])
	}

	return nil
}

func (w *Wallet) handleLockRelease(msg *Message) error {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()

	if w.isLocked && w.lockHolder == msg.From {
		w.isLocked = false
		w.lockHolder = ""
		fmt.Printf("Lock released by %s\n", msg.From)
	}

	return nil
}

func (w *Wallet) acquireDistributedLock() (string, error) {
	requestID := uuid.New().String()

	lockData := LockRequestData{
		RequestID: requestID,
		NodeID:    w.profile.ID,
		Timestamp: time.Now().Unix(),
	}

	data, _ := json.Marshal(lockData)

	respCh := make(chan bool, 100)
	w.lockMu.Lock()
	w.pendingLocks[requestID] = respCh
	w.lockMu.Unlock()

	defer func() {
		w.lockMu.Lock()
		delete(w.pendingLocks, requestID)
		w.lockMu.Unlock()
		close(respCh)
	}()

	w.mu.RLock()
	nodeIPs := make([]string, 0, len(w.table.Wallets))
	for _, wallet := range w.table.Wallets {
		if wallet.ID != w.profile.ID {
			nodeIPs = append(nodeIPs, wallet.IP)
		}
	}
	w.mu.RUnlock()

	if len(nodeIPs) == 0 {
		return requestID, nil
	}

	fmt.Printf("Sending lock requests to %d nodes...\n", len(nodeIPs))

	// Send lock request to each node individually
	var wg sync.WaitGroup
	for _, ip := range nodeIPs {
		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			msg := NewMessage(MsgLockRequest, w.profile.ID, w.profile.IP)
			msg.Data = data
			
			// Send with retry
			for i := 0; i < 3; i++ {
				w.network.SendTo(msg, targetIP)
				time.Sleep(100 * time.Millisecond)
			}
		}(ip)
	}
	wg.Wait()

	granted := 0
	timeout := time.After(10 * time.Second)

	for granted < len(nodeIPs) {
		select {
		case <-respCh:
			granted++
			fmt.Printf("Lock granted %d/%d\n", granted, len(nodeIPs))
		case <-timeout:
			return "", fmt.Errorf("failed to acquire lock from all nodes (got %d/%d)", granted, len(nodeIPs))
		}
	}

	fmt.Println("All locks granted!")
	return requestID, nil
}

func (w *Wallet) releaseDistributedLock() {
	msg := NewMessage(MsgLockRelease, w.profile.ID, w.profile.IP)
	w.network.Broadcast(msg)
}

func (w *Wallet) sendTransaction(toID string, amount int) error {
	w.mu.RLock()
	fromWallet, ok := w.table.Wallets[w.profile.ID]
	if !ok || fromWallet.Balance < amount {
		w.mu.RUnlock()
		return fmt.Errorf("insufficient balance")
	}

	if _, ok := w.table.Wallets[toID]; !ok {
		w.mu.RUnlock()
		return fmt.Errorf("recipient wallet not found: %s", toID)
	}

	nodeIPs := make([]string, 0, len(w.table.Wallets))
	for _, wallet := range w.table.Wallets {
		nodeIPs = append(nodeIPs, wallet.IP)
	}
	w.mu.RUnlock()

	fmt.Printf("Target nodes for transaction: %v\n", nodeIPs)
	fmt.Println("Acquiring distributed lock...")

	_, err := w.acquireDistributedLock()
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %v", err)
	}
	defer w.releaseDistributedLock()

	fmt.Println("Lock acquired, processing transaction...")

	txData := TransactionData{
		From:   w.profile.ID,
		To:     toID,
		Amount: amount,
		TxID:   uuid.New().String(),
	}

	data, _ := json.Marshal(txData)
	txMsg := NewMessage(MsgTransaction, w.profile.ID, w.profile.IP)
	txMsg.Data = data

	fmt.Printf("Broadcasting transaction to %d nodes: %v\n", len(nodeIPs), nodeIPs)
	if err := w.network.BroadcastWithRetry(txMsg, nodeIPs, MaxRetries); err != nil {
		return fmt.Errorf("failed to broadcast transaction: %v", err)
	}

	w.mu.Lock()
	w.table.Wallets[w.profile.ID].Balance -= amount
	w.table.Wallets[toID].Balance += amount
	w.table.Version++
	w.table.Timestamp = time.Now().Unix()
	w.mu.Unlock()

	fmt.Println("Transaction broadcasted, syncing table...")

	syncData := TableSyncData{
		Table:   w.table,
		Version: w.table.Version,
	}

	syncDataBytes, _ := json.Marshal(syncData)
	syncMsg := NewMessage(MsgTableSync, w.profile.ID, w.profile.IP)
	syncMsg.Data = syncDataBytes

	if err := w.network.BroadcastWithRetry(syncMsg, nodeIPs, MaxRetries); err != nil {
		return fmt.Errorf("failed to sync table: %v", err)
	}

	fmt.Println("Transaction completed successfully!")
	return nil
}

func (w *Wallet) runCLI() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== Wallet CLI ===")
	fmt.Println("Commands:")
	fmt.Println("  send <id> <amount> - Send coins to another wallet")
	fmt.Println("  balance            - Show your balance")
	fmt.Println("  table              - Show network table")
	fmt.Println("  exit               - Exit wallet")
	fmt.Println()

	for {
		fmt.Print("> ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		parts := strings.Fields(input)
		cmd := parts[0]

		switch cmd {
		case "send":
			if len(parts) != 3 {
				fmt.Println("Usage: send <id> <amount>")
				continue
			}

			toID := parts[1]
			amount, err := strconv.Atoi(parts[2])
			if err != nil {
				fmt.Println("Invalid amount")
				continue
			}

			if err := w.sendTransaction(toID, amount); err != nil {
				fmt.Printf("Transaction failed: %v\n", err)
			}

		case "balance":
			w.mu.RLock()
			if wallet, ok := w.table.Wallets[w.profile.ID]; ok {
				fmt.Printf("Your balance: %d coins\n", wallet.Balance)
			} else {
				fmt.Println("Wallet not found in table")
			}
			w.mu.RUnlock()

		case "table":
			w.mu.RLock()
			fmt.Println("\n=== Network Table ===")
			fmt.Printf("Version: %d\n", w.table.Version)
			fmt.Println("Wallets:")
			for _, wallet := range w.table.Wallets {
				marker := ""
				if wallet.ID == w.profile.ID {
					marker = " (YOU)"
				}
				fmt.Printf("  %s (%s): %d coins%s\n", wallet.ID, wallet.IP, wallet.Balance, marker)
			}
			fmt.Println()
			w.mu.RUnlock()

		case "exit":
			fmt.Println("Exiting...")
			w.network.Close()
			return

		default:
			fmt.Println("Unknown command")
		}
	}
}

func main() {
	w, err := NewWallet()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := w.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
