package wallet

import (
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
)
func GetLocalIP() (string, error) {
	// Check if IP is specified via environment variable
	if envIP := os.Getenv("GRIM_IP"); envIP != "" {
		log.Printf("Using IP from GRIM_IP environment variable: %s", envIP)
		return envIP, nil
	}

	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %w", err)
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
		return "", fmt.Errorf("no suitable network interface found")
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
func GenerateWalletID() (int, error) {

	interfaces, err := net.Interfaces()
	if err != nil {
		return 0, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	for _, iface := range interfaces {

		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		id := macToID(mac)
		if id > 0 {
			return id, nil
		}
	}
	return generateRandomID()
}
func macToID(mac string) int {

	clean := strings.ReplaceAll(mac, ":", "")
	if len(clean) >= 6 {

		suffix := clean[len(clean)-6:]
		if id, err := strconv.ParseInt(suffix, 16, 64); err == nil {

			return int(id%999999) + 1
		}
	}
	return 0
}
func generateRandomID() (int, error) {

	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return 0, fmt.Errorf("failed to generate random bytes: %w", err)
	}
	id := int(bytes[0])<<24 + int(bytes[1])<<16 + int(bytes[2])<<8 + int(bytes[3])
	if id < 0 {
		id = -id
	}
	return (id % 999999) + 1, nil
}
