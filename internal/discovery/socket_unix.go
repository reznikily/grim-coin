//go:build !windows

package discovery

import (
	"log"
	"net"
	"syscall"
)

func setBroadcastOption(conn *net.UDPConn) {
	file, err := conn.File()
	if err == nil {
		fd := int(file.Fd())
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
			log.Printf("Warning: failed to set SO_BROADCAST: %v", err)
		}
		file.Close()
	}
}
