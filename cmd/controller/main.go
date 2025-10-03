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

	localIP, err := getLocalIP()
	if err != nil {
		log.Fatalf("Failed to get local IP: %v", err)
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
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable IP found")
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
