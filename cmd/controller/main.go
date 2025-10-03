package main

import (
	"bufio"
	"context"
	"fmt"
	"grim-coin/internal/controller"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	localIP := os.Getenv("GRIM_IP")
	if localIP == "" {
		// Print all available network interfaces and let user choose
		var err error
		localIP, err = selectNetworkInterface()
		if err != nil {
			log.Fatalf("Failed to get local IP: %v", err)
		}
	} else {
		log.Printf("Using IP from GRIM_IP environment variable: %s", localIP)
	}

	log.Println("=== GrimCoin Controller ===")
	log.Printf("Local IP: %s", localIP)
	log.Println("- P1 (wallets): :8000")
	log.Println("- P2 (observers): :8080")
	log.Println("- Broadcasting on UDP :7999")

	server := controller.NewServer()

	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	if err := server.Start(ctx, localIP); err != nil {
		log.Fatalf("Controller failed: %v", err)
	}

	log.Println("Controller shutdown complete")
}

func getLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	var candidateIPs []net.IP

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
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
				candidateIPs = append(candidateIPs, ip)
			}
		}
	}

	if len(candidateIPs) == 0 {
		return "", fmt.Errorf("no suitable IP found")
	}

	// Prefer 192.168.x.x and 10.x.x.x over 172.x.x.x (often virtual adapters)
	for _, ip := range candidateIPs {
		if ip[0] == 192 || ip[0] == 10 {
			return ip.String(), nil
		}
	}

	// Return first candidate if no preferred IP found
	return candidateIPs[0].String(), nil
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
