package main

import (
	"context"
	"fmt"
	"grim-coin/internal/controller"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Print all available network interfaces
	printNetworkInterfaces()

	localIP := os.Getenv("GRIM_IP")
	if localIP == "" {
		var err error
		localIP, err = getLocalIP()
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

func printNetworkInterfaces() {
	log.Println("Available network interfaces:")
	interfaces, err := net.Interfaces()
	if err != nil {
		log.Printf("Failed to get interfaces: %v", err)
		return
	}

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

			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				private := ""
				if isPrivateIP(ip) {
					private = " (private)"
				}
				log.Printf("  - %s: %s%s", iface.Name, ip.String(), private)
			}
		}
	}
	log.Println("To use specific IP, set GRIM_IP environment variable")
	log.Println("")
}
