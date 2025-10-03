package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"grim-coin/internal/wallet"
	"log"
	"net"
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

	// Check if IP should be selected interactively
	if os.Getenv("GRIM_IP") == "" {
		selectedIP, err := selectNetworkInterface()
		if err != nil {
			log.Fatalf("Failed to select network interface: %v", err)
		}
		os.Setenv("GRIM_IP", selectedIP)
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

type networkInterface struct {
	name string
	ip   string
}

func selectNetworkInterface() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %w", err)
	}

	var availableIPs []networkInterface

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && ip.To4() != nil && !ip.IsLoopback() && isPrivateIP(ip) {
				availableIPs = append(availableIPs, networkInterface{
					name: iface.Name,
					ip:   ip.String(),
				})
			}
		}
	}

	if len(availableIPs) == 0 {
		return "", fmt.Errorf("no suitable network interfaces found")
	}

	// If only one interface, use it automatically
	if len(availableIPs) == 1 {
		log.Printf("Using network interface: %s (%s)", availableIPs[0].name, availableIPs[0].ip)
		return availableIPs[0].ip, nil
	}

	// Show available interfaces
	fmt.Println("\n=== Available Network Interfaces ===")
	for i, iface := range availableIPs {
		fmt.Printf("  [%d] %s: %s", i+1, iface.name, iface.ip)
		// Mark preferred networks
		ip := net.ParseIP(iface.ip)
		if ip != nil && (ip[0] == 192 || ip[0] == 10) {
			fmt.Print(" (recommended)")
		}
		fmt.Println()
	}
	fmt.Println()

	// Get user choice
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Select interface [1-%d] or press Enter for auto-select: ", len(availableIPs))
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}

		input = strings.TrimSpace(input)

		// Auto-select if empty
		if input == "" {
			// Prefer 192.168.x.x and 10.x.x.x
			for _, iface := range availableIPs {
				ip := net.ParseIP(iface.ip)
				if ip != nil && (ip[0] == 192 || ip[0] == 10) {
					log.Printf("Auto-selected: %s (%s)", iface.name, iface.ip)
					return iface.ip, nil
				}
			}
			// Return first if no preferred found
			log.Printf("Auto-selected: %s (%s)", availableIPs[0].name, availableIPs[0].ip)
			return availableIPs[0].ip, nil
		}

		// Parse selection
		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(availableIPs) {
			fmt.Printf("Invalid choice. Please enter a number between 1 and %d.\n", len(availableIPs))
			continue
		}

		selected := availableIPs[choice-1]
		log.Printf("Selected: %s (%s)", selected.name, selected.ip)
		return selected.ip, nil
	}
}

func isPrivateIP(ip net.IP) bool {
	private := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}

	for _, block := range private {
		_, subnet, _ := net.ParseCIDR(block)
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}
