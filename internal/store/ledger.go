package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)
type Ledger struct {
	Balances map[int]int `json:"balances"`
	Version  int         `json:"version"`
	mutex    sync.RWMutex
	filePath string
}
func NewLedger(dataPath string, walletID int) (*Ledger, error) {

	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	ledger := &Ledger{
		Balances: make(map[int]int),
		Version:  0,
		filePath: dataPath,
	}
	if loadErr := ledger.Load(); loadErr != nil {

		if os.IsNotExist(loadErr) {
			ledger.Balances[walletID] = 10
			if err := ledger.Save(); err != nil {
				return nil, fmt.Errorf("failed to save initial ledger: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to load ledger: %w", loadErr)
		}
	}
	if _, exists := ledger.Balances[walletID]; !exists {
		ledger.Balances[walletID] = 10
		if err := ledger.Save(); err != nil {
			return nil, fmt.Errorf("failed to save ledger after adding wallet: %w", err)
		}
	}

	return ledger, nil
}
func (l *Ledger) GetBalance(walletID int) int {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	balance, exists := l.Balances[walletID]
	if !exists {
		return 0
	}
	return balance
}
func (l *Ledger) GetVersion() int {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	return l.Version
}
func (l *Ledger) UpdateBalance(walletID, newBalance int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Balances[walletID] = newBalance
	return l.Save()
}
func (l *Ledger) SetVersion(version int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Version = version
	return l.Save()
}
func (l *Ledger) GetSnapshot() (map[int]int, int) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	snapshot := make(map[int]int)
	for id, balance := range l.Balances {
		snapshot[id] = balance
	}

	return snapshot, l.Version
}
func (l *Ledger) AddWallet(walletID int, initialBalance int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if _, exists := l.Balances[walletID]; exists {
		return nil
	}

	l.Balances[walletID] = initialBalance
	return l.Save()
}
func (l *Ledger) Save() error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ledger: %w", err)
	}
	tmpFile := l.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}
	if err := os.Rename(tmpFile, l.filePath); err != nil {

		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}

	return nil
}

func (l *Ledger) Load() error {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		return err
	}

	var loaded Ledger
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to unmarshal ledger: %w", err)
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Balances = loaded.Balances
	l.Version = loaded.Version

	if l.Balances == nil {
		l.Balances = make(map[int]int)
	}

	return nil
}

func (l *Ledger) ApplyTransaction(from, to, amount, expectedVersion int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.Version != expectedVersion {
		return fmt.Errorf("version mismatch: expected %d, got %d", expectedVersion, l.Version)
	}

	fromBalance := l.Balances[from]
	if fromBalance < amount {
		return fmt.Errorf("insufficient balance: %d < %d", fromBalance, amount)
	}

	l.Balances[from] = fromBalance - amount
	l.Balances[to] = l.Balances[to] + amount
	l.Version++

	return l.Save()
}
