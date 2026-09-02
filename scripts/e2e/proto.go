package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// --- SOCKS5 client (RFC 1928) ---

func socks5Connect(socksAddr, target string, user, pass string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if err := socks5Handshake(c, user, pass); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := socks5Request(c, 0x01, target); err != nil {
		_ = c.Close()
		return nil, err
	}
	if _, err := readSocksReply(c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func socks5Handshake(c net.Conn, user, pass string) error {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	methods := []byte{0x00}
	if user != "" {
		methods = []byte{0x02, 0x00}
	}
	req := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := c.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("bad socks version %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
		return nil
	case 0x02:
		if user == "" {
			return errors.New("server demanded auth but none configured")
		}
		sub := []byte{0x01, byte(len(user))}
		sub = append(sub, user...)
		sub = append(sub, byte(len(pass)))
		sub = append(sub, pass...)
		if _, err := c.Write(sub); err != nil {
			return err
		}
		ack := make([]byte, 2)
		if _, err := io.ReadFull(c, ack); err != nil {
			return err
		}
		if ack[1] != 0x00 {
			return fmt.Errorf("socks auth rejected (status %d)", ack[1])
		}
		return nil
	case 0xff:
		return errors.New("no acceptable socks auth method")
	default:
		return fmt.Errorf("unexpected socks method %d", resp[1])
	}
}

func socks5Request(c net.Conn, cmd byte, target string) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	req := []byte{0x05, cmd, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	_, err = c.Write(req)
	return err
}

// readSocksReply parses a reply and returns the bound address.
func readSocksReply(c net.Conn) (string, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return "", err
	}
	if head[1] != 0x00 {
		return "", fmt.Errorf("socks request failed with reply code %d", head[1])
	}
	var addr string
	switch head[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		addr = net.IP(b).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		addr = string(b)
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		addr = net.IP(b).String()
	default:
		return "", fmt.Errorf("unknown socks address type %d", head[3])
	}
	p := make([]byte, 2)
	if _, err := io.ReadFull(c, p); err != nil {
		return "", err
	}
	return net.JoinHostPort(addr, strconv.Itoa(int(binary.BigEndian.Uint16(p)))), nil
}

// socks5UDPAssociate opens an associate and returns the relay address plus the
// TCP control connection, which must stay open for the association to live.
func socks5UDPAssociate(socksAddr string) (net.Conn, string, error) {
	c, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		return nil, "", err
	}
	if err := socks5Handshake(c, "", ""); err != nil {
		_ = c.Close()
		return nil, "", err
	}
	if err := socks5Request(c, 0x03, "0.0.0.0:0"); err != nil {
		_ = c.Close()
		return nil, "", err
	}
	relay, err := readSocksReply(c)
	if err != nil {
		_ = c.Close()
		return nil, "", err
	}
	host, port, _ := net.SplitHostPort(relay)
	if host == "0.0.0.0" || host == "::" || host == "" {
		sh, _, _ := net.SplitHostPort(socksAddr)
		relay = net.JoinHostPort(sh, port)
	}
	return c, relay, nil
}

// encapsulateUDP wraps a payload in a SOCKS5 UDP request header.
func encapsulateUDP(target string, payload []byte) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	pkt := []byte{0x00, 0x00, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		pkt = append(pkt, 0x01)
		pkt = append(pkt, ip.To4()...)
	} else {
		pkt = append(pkt, 0x03, byte(len(host)))
		pkt = append(pkt, host...)
	}
	pkt = binary.BigEndian.AppendUint16(pkt, uint16(port))
	return append(pkt, payload...), nil
}

// decapsulateUDP strips the SOCKS5 UDP header.
func decapsulateUDP(pkt []byte) ([]byte, error) {
	if len(pkt) < 10 {
		return nil, fmt.Errorf("short udp packet (%d bytes)", len(pkt))
	}
	off := 3
	switch pkt[off] {
	case 0x01:
		off += 1 + 4
	case 0x03:
		off += 1 + 1 + int(pkt[off+1])
	case 0x04:
		off += 1 + 16
	default:
		return nil, fmt.Errorf("unknown udp address type %d", pkt[off])
	}
	off += 2
	if off > len(pkt) {
		return nil, errors.New("truncated udp header")
	}
	return pkt[off:], nil
}
