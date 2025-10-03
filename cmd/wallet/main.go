package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"grim-coin/internal/wallet"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v2"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	configPath := flag.String("config", "", "Path to wallet config file (required)")
	flag.Parse()

	if *configPath == "" {
		flag.Usage()
		log.Fatal("Config file is required")
	}
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("=== GrimCoin Wallet ===")
	log.Printf("P2P Port: %s", config.ListenP3)
	log.Println("Waiting for network discovery...")
	client, err := wallet.NewClient(config, "data")
	if err != nil {
		log.Fatalf("Failed to create wallet client: %v", err)
	}

	log.Printf("Current balance: %d, version: %d", client.GetBalance(), client.GetVersion())
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	go func() {
		if err := client.Start(ctx); err != nil {
			log.Fatalf("Wallet failed: %v", err)
		}
	}()

	go handleCommands(ctx, client)

	<-ctx.Done()
	log.Println("Wallet shutdown complete")
}
func loadConfig(configPath string) (*wallet.Config, error) {

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var config wallet.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	if config.ListenP3 == "" {
		config.ListenP3 = "0.0.0.0:9000"
	}

	log.Printf("Loaded config from: %s", configPath)
	return &config, nil
}

func handleCommands(ctx context.Context, client *wallet.Client) {
	reader := bufio.NewReader(os.Stdin)
	
	fmt.Println("\nCommands:")
	fmt.Println("  send <to_id> <amount> - Send coins")
	fmt.Println("  balance               - Show balance")
	fmt.Println("  exit                  - Exit wallet")
	fmt.Println()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			continue
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		switch parts[0] {
		case "send":
			if len(parts) != 3 {
				fmt.Println("Usage: send <to_id> <amount>")
				continue
			}

			toID, err := strconv.Atoi(parts[1])
			if err != nil {
				fmt.Println("Invalid to_id")
				continue
			}

			amount, err := strconv.Atoi(parts[2])
			if err != nil {
				fmt.Println("Invalid amount")
				continue
			}

			if err := client.InitiateTransaction(toID, amount); err != nil {
				fmt.Printf("Transaction failed: %v\n", err)
			} else {
				fmt.Println("Transaction initiated")
			}

		case "balance":
			fmt.Printf("Balance: %d, Version: %d\n", client.GetBalance(), client.GetVersion())

		case "exit":
			fmt.Println("Exiting...")
			return

		default:
			fmt.Println("Unknown command")
		}
	}
}
