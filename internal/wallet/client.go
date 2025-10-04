package wallet

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"grim-coin/internal/discovery"
	"grim-coin/internal/protocol"
	"grim-coin/internal/store"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)
type Config struct {
	ListenP3 string `yaml:"listen_p3,omitempty"`
}
type WalletProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type Client struct {
	config          *Config
	profile         *WalletProfile
	ledger          *store.Ledger
	conn            *websocket.Conn
	mutex           sync.RWMutex
	connected       bool
	retryCount      int
	maxRetries      int
	profilePath     string
	peerConns       map[string]*websocket.Conn
	peerMutex       sync.RWMutex
	activeTxID      string
	activeTxLock    sync.Mutex
	controllerAddr  string
	discoveredPeers map[int]string
	peersMutex      sync.RWMutex
}

func NewClient(config *Config, dataDir string) (*Client, error) {
	if config.ListenP3 == "" {
		config.ListenP3 = "0.0.0.0:9000"
	}

	client := &Client{
		config:          config,
		maxRetries:      3,
		profilePath:     fmt.Sprintf("%s/profile.json", dataDir),
		peerConns:       make(map[string]*websocket.Conn),
		discoveredPeers: make(map[int]string),
	}
	profile, err := client.loadOrCreateProfile()
	if err != nil {
		return nil, fmt.Errorf("failed to setup wallet profile: %w", err)
	}
	client.profile = profile
	ledgerPath := fmt.Sprintf("%s/ledger-%d.json", dataDir, profile.ID)
	ledger, err := store.NewLedger(ledgerPath, profile.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger: %w", err)
	}
	client.ledger = ledger

	return client, nil
}
func (c *Client) loadOrCreateProfile() (*WalletProfile, error) {

	if err := os.MkdirAll(filepath.Dir(c.profilePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}
	if profile, err := c.loadProfile(); err == nil {
		log.Printf("Loaded profile: %s (ID=%d)", profile.Name, profile.ID)
		currentIP, err := GetLocalIP()
		if err == nil && currentIP != profile.IP {
			profile.IP = currentIP
			if saveErr := c.saveProfile(profile); saveErr != nil {
				log.Printf("Warning: failed to save updated profile: %v", saveErr)
			}
		}

		return profile, nil
	}
	log.Println("Creating new wallet profile...")
	return c.createNewProfile()
}
func (c *Client) loadProfile() (*WalletProfile, error) {
	data, err := os.ReadFile(c.profilePath)
	if err != nil {
		return nil, err
	}

	var profile WalletProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal profile: %w", err)
	}

	return &profile, nil
}
func (c *Client) saveProfile(profile *WalletProfile) error {
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}
	tmpFile := c.profilePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary profile file: %w", err)
	}

	if err := os.Rename(tmpFile, c.profilePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary profile file: %w", err)
	}

	return nil
}
func (c *Client) createNewProfile() (*WalletProfile, error) {

	id, err := GenerateWalletID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate wallet ID: %w", err)
	}
	ip, err := GetLocalIP()
	if err != nil {
		return nil, fmt.Errorf("failed to detect IP address: %w", err)
	}
	name, err := c.promptForName()
	if err != nil {
		return nil, fmt.Errorf("failed to get user name: %w", err)
	}

	profile := &WalletProfile{
		ID:   id,
		Name: name,
		IP:   ip,
	}
	if err := c.saveProfile(profile); err != nil {
		return nil, fmt.Errorf("failed to save new profile: %w", err)
	}

	log.Printf("Created profile: %s (ID=%d)", profile.Name, profile.ID)
	return profile, nil
}
func (c *Client) promptForName() (string, error) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("Добро пожаловать в GrimCoin! Введите ваше имя: ")
		name, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}

		name = strings.TrimSpace(name)
		if len(name) == 0 {
			fmt.Println("Имя не может быть пустым. Попробуйте еще раз.")
			continue
		}

		if len(name) > 50 {
			fmt.Println("Имя слишком длинное (максимум 50 символов). Попробуйте еще раз.")
			continue
		}
		fmt.Printf("Вы ввели: %s. Правильно? (y/n): ", name)
		confirm, _ := reader.ReadString('\n')
		confirm = strings.TrimSpace(strings.ToLower(confirm))

		if confirm == "y" || confirm == "yes" || confirm == "д" || confirm == "да" {
			return name, nil
		}
	}
}
func (c *Client) Start(ctx context.Context) error {
	// Starting wallet

	// Use the IP from profile (which was already determined during loadOrCreateProfile)
	disc, err := discovery.NewServiceWithIP(c.profile.IP)
	if err != nil {
		return err
	}
	defer disc.Close()

	go disc.StartListening()
	announcements := disc.Subscribe()

	go c.handleDiscovery(ctx, announcements, disc)
	go c.startP2PServer(ctx)
	go c.connectToController(ctx)
	go c.connectToPeers(ctx)

	<-ctx.Done()

	// Shutting down wallet
	c.disconnect()

	return nil
}
func (c *Client) handleDiscovery(ctx context.Context, announcements <-chan *discovery.Announcement, disc *discovery.Service) {
	localIP := c.profile.IP

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ann := <-announcements:
			if ann.Type == discovery.ServiceController {
				addr := fmt.Sprintf("ws://%s:%d/ws", ann.IP, ann.PortP1)
				if c.controllerAddr != addr {
					// Discovered controller - no log to avoid spam
					c.controllerAddr = addr
				}
			} else if ann.Type == discovery.ServiceWallet && ann.ID != c.profile.ID {
				peerAddr := fmt.Sprintf("ws://%s:%d/ws", ann.IP, ann.PortP3)
				c.peersMutex.Lock()
				if _, exists := c.discoveredPeers[ann.ID]; !exists {
					// Discovered new peer - no log to avoid spam
					c.discoveredPeers[ann.ID] = peerAddr
					go c.connectToPeer(ctx, peerAddr)
				}
				c.peersMutex.Unlock()
			}
		case <-ticker.C:
			disc.Announce(&discovery.Announcement{
				Type:   discovery.ServiceWallet,
				ID:     c.profile.ID,
				Name:   c.profile.Name,
				IP:     localIP,
				PortP3: 9000,
			})
		}
	}
}

func (c *Client) connectToController(ctx context.Context) {
	backoffDurations := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if c.controllerAddr == "" {
			time.Sleep(1 * time.Second)
			continue
		}

		if err := c.connect(); err != nil {
			c.retryCount++
			// Reduce log spam - only log every few attempts
			if c.retryCount == 1 || c.retryCount >= c.maxRetries {
				log.Printf("Failed to connect to controller (attempt %d/%d): %v",
					c.retryCount, c.maxRetries, err)
			}

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
		c.retryCount = 0
		// Connected to controller
		if err := c.sendHello(); err != nil {
			log.Printf("Failed to send hello: %v", err)
			c.disconnect()
			continue
		}
		c.handleMessages(ctx)
		// Connection lost, reconnecting...
		c.disconnect()
		time.Sleep(1 * time.Second)
	}
}
func (c *Client) connect() error {
	if c.controllerAddr == "" {
		return fmt.Errorf("controller not discovered yet")
	}

	u, err := url.Parse(c.controllerAddr)
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
func (c *Client) disconnect() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connected = false
}
func (c *Client) sendHello() error {
	balance := c.ledger.GetBalance(c.profile.ID)
	version := c.ledger.GetVersion()

	hello := protocol.HelloPayload{
		ID:      c.profile.ID,
		Name:    c.profile.Name,
		IP:      c.profile.IP,
		Balance: balance,
		Version: version,
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeHello, c.profile.ID, hello)
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

	// Hello sent

	return nil
}
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

		// Received message (logging disabled to avoid spam)

		switch envelope.Type {
		case protocol.MsgTypeAck:
			c.handleAckMessage(&envelope)
		case protocol.MsgTypeError:
			c.handleErrorMessage(&envelope)
		case protocol.MsgTypeLockGranted:
			c.handleLockGranted(&envelope)
		case protocol.MsgTypeStatePushRequest:
			c.handleStatePushRequest(&envelope)
		default:
			log.Printf("Unknown message type: %s", envelope.Type)
		}
	}
}
func (c *Client) handleAckMessage(envelope *protocol.Envelope) {
	var ack protocol.AckPayload
	if err := envelope.ParsePayload(&ack); err != nil {
		log.Printf("Failed to parse ack payload: %v", err)
		return
	}

	// Ack received (logging disabled to avoid spam)
	_ = ack
}
func (c *Client) handleErrorMessage(envelope *protocol.Envelope) {
	var errorPayload protocol.ErrorPayload
	if err := envelope.ParsePayload(&errorPayload); err != nil {
		log.Printf("Failed to parse error payload: %v", err)
		return
	}

	log.Printf("Received error from controller: [%d] %s - %s",
		errorPayload.Code, errorPayload.Message, errorPayload.Details)
}
func (c *Client) UpdateBalance(newBalance int) error {

	if err := c.ledger.UpdateBalance(c.profile.ID, newBalance); err != nil {
		return fmt.Errorf("failed to update local balance: %w", err)
	}
	stateUpdate := protocol.StateUpdatePayload{
		Balance: newBalance,
		Version: c.ledger.GetVersion(),
	}

	envelope, err := protocol.CreateEnvelope(protocol.MsgTypeStateUpdate, c.profile.ID, stateUpdate)
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

	// State update sent (no log to avoid spam)
	return nil
}
func (c *Client) GetBalance() int {
	return c.ledger.GetBalance(c.profile.ID)
}
func (c *Client) GetVersion() int {
	return c.ledger.GetVersion()
}

func (c *Client) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.connected
}

func (c *Client) startP2PServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", c.handleP2PConnection)
	
	server := &http.Server{
		Addr:    c.config.ListenP3,
		Handler: mux,
	}

	go func() {
		// P2P server starting
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("P2P server error: %v", err)
		}
	}()

	<-ctx.Done()
	server.Shutdown(context.Background())
}

func (c *Client) handleP2PConnection(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade P2P connection: %v", err)
		return
	}
	defer conn.Close()

	for {
		var envelope protocol.Envelope
		if err := conn.ReadJSON(&envelope); err != nil {
			break
		}

		switch envelope.Type {
		case protocol.MsgTypeTransaction:
			c.handleIncomingTransaction(&envelope, conn)
		}
	}
}

func (c *Client) connectToPeers(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.peersMutex.RLock()
			peers := make([]string, 0, len(c.discoveredPeers))
			for _, addr := range c.discoveredPeers {
				peers = append(peers, addr)
			}
			c.peersMutex.RUnlock()

			c.peerMutex.RLock()
			connectedCount := len(c.peerConns)
			c.peerMutex.RUnlock()

			if connectedCount < len(peers) {
				for _, peerURL := range peers {
					c.peerMutex.RLock()
					_, exists := c.peerConns[peerURL]
					c.peerMutex.RUnlock()
					
					if !exists {
						go c.connectToPeer(ctx, peerURL)
					}
				}
			}
		}
	}
}

func (c *Client) connectToPeer(ctx context.Context, peerURL string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(peerURL, nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		c.peerMutex.Lock()
		c.peerConns[peerURL] = conn
		c.peerMutex.Unlock()

		// Connected to peer
		
		for {
			var envelope protocol.Envelope
			if err := conn.ReadJSON(&envelope); err != nil {
				break
			}

			switch envelope.Type {
			case protocol.MsgTypeTransaction:
				c.handleIncomingTransaction(&envelope, conn)
			}
		}

		c.peerMutex.Lock()
		delete(c.peerConns, peerURL)
		c.peerMutex.Unlock()

		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func (c *Client) handleIncomingTransaction(envelope *protocol.Envelope, conn *websocket.Conn) {
	var tx protocol.TransactionPayload
	if err := envelope.ParsePayload(&tx); err != nil {
		log.Printf("Failed to parse transaction: %v", err)
		return
	}

	if err := c.ledger.ApplyTransaction(tx.From, tx.To, tx.Amount, tx.Version-1); err != nil {
		log.Printf("Failed to apply transaction: %v", err)
		c.sendAckToPeer(conn, envelope.TxID, false)
		return
	}

	log.Printf("✓ Received %d coins from wallet %d", tx.Amount, tx.From)
	c.sendAckToPeer(conn, envelope.TxID, true)
}

func (c *Client) sendAckToPeer(conn *websocket.Conn, txID string, success bool) {
	ack := protocol.AckPayload{
		MessageType: protocol.MsgTypeTransaction,
		Success:     success,
	}
	
	env, err := protocol.CreateEnvelope(protocol.MsgTypeAck, c.profile.ID, ack)
	if err != nil {
		log.Printf("Failed to create ack envelope: %v", err)
		return
	}
	env.TxID = txID
	
	if err := conn.WriteJSON(env); err != nil {
		log.Printf("Failed to send ack to peer: %v", err)
	}
}

func (c *Client) handleLockGranted(envelope *protocol.Envelope) {
	var granted protocol.LockGrantedPayload
	if err := envelope.ParsePayload(&granted); err != nil {
		log.Printf("Failed to parse lock granted: %v", err)
		return
	}

	// Lock granted (no log to avoid spam)
}

func (c *Client) handleStatePushRequest(envelope *protocol.Envelope) {
	balances, version := c.ledger.GetSnapshot()
	
	resp := protocol.StatePushResponsePayload{
		Balances: balances,
		Version:  version,
	}
	
	env, err := protocol.CreateEnvelope(protocol.MsgTypeStatePushResponse, c.profile.ID, resp)
	if err != nil {
		log.Printf("Failed to create state push response envelope: %v", err)
		return
	}
	env.TxID = envelope.TxID
	
	c.mutex.RLock()
	conn := c.conn
	c.mutex.RUnlock()
	
	if conn != nil {
		if err := conn.WriteJSON(env); err != nil {
			log.Printf("Failed to send state push response: %v", err)
		}
	}
}

func (c *Client) InitiateTransaction(to, amount int) error {
	txID := fmt.Sprintf("tx-%d-%d", c.profile.ID, time.Now().UnixNano())
	
	req := protocol.LockRequestPayload{TxID: txID}
	env, _ := protocol.CreateEnvelope(protocol.MsgTypeLockRequest, c.profile.ID, req)
	
	c.mutex.RLock()
	conn := c.conn
	c.mutex.RUnlock()
	
	if conn == nil {
		return fmt.Errorf("not connected to controller")
	}
	
	if err := conn.WriteJSON(env); err != nil {
		return err
	}

	time.Sleep(500 * time.Millisecond)

	version := c.ledger.GetVersion()
	tx := protocol.TransactionPayload{
		From:    c.profile.ID,
		To:      to,
		Amount:  amount,
		Version: version + 1,
	}

	txEnv, _ := protocol.CreateEnvelope(protocol.MsgTypeTransaction, c.profile.ID, tx)
	txEnv.TxID = txID

	if err := c.ledger.ApplyTransaction(tx.From, tx.To, tx.Amount, version); err != nil {
		return err
	}

	c.peerMutex.RLock()
	peers := make([]*websocket.Conn, 0, len(c.peerConns))
	for _, p := range c.peerConns {
		peers = append(peers, p)
	}
	c.peerMutex.RUnlock()

	// Send to peers concurrently with WaitGroup
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p *websocket.Conn) {
			defer wg.Done()
			if err := p.WriteJSON(txEnv); err != nil {
				log.Printf("Failed to send transaction to peer: %v", err)
			}
		}(peer)
	}
	
	// Wait for all sends to complete
	wg.Wait()

	time.Sleep(1 * time.Second)

	commit := protocol.CommitNoticePayload{TxID: txID}
	commitEnv, _ := protocol.CreateEnvelope(protocol.MsgTypeCommitNotice, c.profile.ID, commit)
	
	if err := conn.WriteJSON(commitEnv); err != nil {
		return err
	}

	log.Printf("✓ Sent %d coins to wallet %d", amount, to)
	return nil
}
