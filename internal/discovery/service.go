package discovery

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

const (
	BroadcastPort = 7999
)

type ServiceType string

const (
	ServiceController ServiceType = "controller"
	ServiceWallet     ServiceType = "wallet"
)

type Announcement struct {
	Type      ServiceType `json:"type"`
	ID        int         `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	IP        string      `json:"ip"`
	PortP1    int         `json:"port_p1,omitempty"`
	PortP2    int         `json:"port_p2,omitempty"`
	PortP3    int         `json:"port_p3,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

type Service struct {
	conn          *net.UDPConn
	listeners     []chan *Announcement
	mutex         sync.RWMutex
	localIP       string
	broadcastAddr string
}

func NewService() (*Service, error) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", BroadcastPort))
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, err
	}

	setBroadcastOption(conn)

	localIP, broadcastAddr, err := getLocalIPAndBroadcast()
	if err != nil {
		conn.Close()
		return nil, err
	}

	log.Printf("Discovery service: local IP %s, broadcast %s", localIP, broadcastAddr)

	return &Service{
		conn:          conn,
		listeners:     make([]chan *Announcement, 0),
		localIP:       localIP,
		broadcastAddr: broadcastAddr,
	}, nil
}

func (s *Service) StartListening() {
	buf := make([]byte, 1024)
	for {
		n, remoteAddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		var ann Announcement
		if err := json.Unmarshal(buf[:n], &ann); err != nil {
			log.Printf("Failed to parse announcement from %s: %v", remoteAddr, err)
			continue
		}

		if ann.IP == s.localIP {
			continue
		}

		log.Printf("Received %s announcement from %s (ID: %d, Name: %s)", 
			ann.Type, ann.IP, ann.ID, ann.Name)

		s.mutex.RLock()
		for _, ch := range s.listeners {
			select {
			case ch <- &ann:
			default:
			}
		}
		s.mutex.RUnlock()
	}
}

func (s *Service) Subscribe() <-chan *Announcement {
	ch := make(chan *Announcement, 10)
	s.mutex.Lock()
	s.listeners = append(s.listeners, ch)
	s.mutex.Unlock()
	return ch
}

func (s *Service) Announce(ann *Announcement) error {
	ann.Timestamp = time.Now().Unix()
	data, err := json.Marshal(ann)
	if err != nil {
		return err
	}

	addr, err := net.ResolveUDPAddr("udp4", s.broadcastAddr)
	if err != nil {
		return err
	}

	localAddr, _ := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
	broadcastConn, err := net.DialUDP("udp4", localAddr, addr)
	if err != nil {
		return err
	}
	defer broadcastConn.Close()

	if err := broadcastConn.SetWriteBuffer(1024); err != nil {
		return err
	}

	n, err := broadcastConn.Write(data)
	if err != nil {
		return err
	}

	log.Printf("Sent %s announcement to %s (%d bytes)", ann.Type, s.broadcastAddr, n)
	return nil
}

func (s *Service) StartAnnouncing(ann *Announcement, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if err := s.Announce(ann); err != nil {
			log.Printf("Failed to announce: %v", err)
		}
	}
}

func (s *Service) Close() error {
	return s.conn.Close()
}

func getLocalIPAndBroadcast() (string, string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		if iface.Flags&net.FlagBroadcast == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || !isPrivateIP(ip) {
				continue
			}

			broadcast := make(net.IP, 4)
			for i := range ip {
				broadcast[i] = ip[i] | ^ipNet.Mask[i]
			}

			broadcastAddr := fmt.Sprintf("%s:%d", broadcast.String(), BroadcastPort)
			return ip.String(), broadcastAddr, nil
		}
	}

	return "", "", fmt.Errorf("no suitable IP found")
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
