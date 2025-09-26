package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Ledger - локальный реестр балансов кошелька
type Ledger struct {
	Balances map[int]int `json:"balances"` // ID -> баланс
	Version  int         `json:"version"`  // текущая версия состояния
	mutex    sync.RWMutex
	filePath string
}

// NewLedger - создает новый ledger с указанным путем к файлу
func NewLedger(dataPath string, walletID int) (*Ledger, error) {
	// Создаем директорию, если её нет
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	ledger := &Ledger{
		Balances: make(map[int]int),
		Version:  0,
		filePath: dataPath,
	}

	// Пытаемся загрузить существующие данные
	if loadErr := ledger.Load(); loadErr != nil {
		// Если файл не существует, создаем с начальными данными
		if os.IsNotExist(loadErr) {
			ledger.Balances[walletID] = 10 // начальный баланс для нового кошелька
			if err := ledger.Save(); err != nil {
				return nil, fmt.Errorf("failed to save initial ledger: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to load ledger: %w", loadErr)
		}
	}

	// Если кошелька нет в ledger, добавляем с начальным балансом
	if _, exists := ledger.Balances[walletID]; !exists {
		ledger.Balances[walletID] = 10
		if err := ledger.Save(); err != nil {
			return nil, fmt.Errorf("failed to save ledger after adding wallet: %w", err)
		}
	}

	return ledger, nil
}

// GetBalance - возвращает баланс указанного кошелька
func (l *Ledger) GetBalance(walletID int) int {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	balance, exists := l.Balances[walletID]
	if !exists {
		return 0
	}
	return balance
}

// GetVersion - возвращает текущую версию
func (l *Ledger) GetVersion() int {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	return l.Version
}

// UpdateBalance - обновляет баланс кошелька
func (l *Ledger) UpdateBalance(walletID, newBalance int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Balances[walletID] = newBalance
	return l.Save()
}

// SetVersion - устанавливает версию
func (l *Ledger) SetVersion(version int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Version = version
	return l.Save()
}

// GetSnapshot - возвращает снимок текущего состояния
func (l *Ledger) GetSnapshot() (map[int]int, int) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	// Создаем копию карты
	snapshot := make(map[int]int)
	for id, balance := range l.Balances {
		snapshot[id] = balance
	}

	return snapshot, l.Version
}

// AddWallet - добавляет новый кошелек с начальным балансом
func (l *Ledger) AddWallet(walletID int, initialBalance int) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if _, exists := l.Balances[walletID]; exists {
		return nil // кошелек уже существует
	}

	l.Balances[walletID] = initialBalance
	return l.Save()
}

// Save - сохраняет ledger в файл (должен вызываться под мьютексом)
func (l *Ledger) Save() error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ledger: %w", err)
	}

	// Атомарная запись: сначала во временный файл
	tmpFile := l.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}

	// Затем переименовываем
	if err := os.Rename(tmpFile, l.filePath); err != nil {
		// Пытаемся удалить временный файл, игнорируем ошибку удаления
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}

	return nil
}

// Load - загружает ledger из файла
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

	// Убеждаемся, что карта инициализирована
	if l.Balances == nil {
		l.Balances = make(map[int]int)
	}

	return nil
}
