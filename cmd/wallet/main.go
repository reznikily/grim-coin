package main

import (
	"context"
	"flag"
	"fmt"
	"grim-coin/internal/wallet"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gopkg.in/yaml.v2"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Парсим аргументы командной строки
	configPath := flag.String("config", "", "Path to wallet config file (required)")
	flag.Parse()

	if *configPath == "" {
		flag.Usage()
		log.Fatal("Config file is required")
	}

	// Читаем конфигурацию
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("=== GrimCoin Wallet %d (%s) ===", config.ID, config.Name)
	log.Printf("IP: %s", config.IP)
	log.Printf("Controller: %s", config.ControllerWS)
	log.Printf("Listen P3: %s", config.ListenP3)
	log.Printf("Peers: %v", config.Peers)

	// Создаем путь к файлу данных ledger
	dataPath := fmt.Sprintf("data/ledger-%d.json", config.ID)

	// Создаем клиент кошелька
	client, err := wallet.NewClient(config, dataPath)
	if err != nil {
		log.Fatalf("Failed to create wallet client: %v", err)
	}

	log.Printf("Current balance: %d, version: %d", client.GetBalance(), client.GetVersion())

	// Создаем контекст для graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())

	// Обрабатываем сигналы для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	// Запускаем клиент
	if err := client.Start(ctx); err != nil {
		log.Fatalf("Wallet failed: %v", err)
	}

	log.Printf("Wallet %d shutdown complete", config.ID)
}

// loadConfig - загружает конфигурацию из YAML файла
func loadConfig(configPath string) (*wallet.Config, error) {
	// Читаем файл
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Парсим YAML
	var config wallet.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Валидируем обязательные поля
	if config.ID == 0 {
		return nil, fmt.Errorf("wallet ID is required")
	}
	if config.Name == "" {
		return nil, fmt.Errorf("wallet name is required")
	}
	if config.IP == "" {
		return nil, fmt.Errorf("wallet IP is required")
	}
	if config.ControllerWS == "" {
		return nil, fmt.Errorf("controller WebSocket URL is required")
	}

	// Устанавливаем значения по умолчанию
	if config.ListenP3 == "" {
		config.ListenP3 = "0.0.0.0:9000"
	}

	log.Printf("Loaded config from: %s", configPath)
	return &config, nil
}
