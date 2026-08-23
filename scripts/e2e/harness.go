package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type proc struct {
	name string
	cmd  *exec.Cmd
	log  string
}

func startSpaceship(bin, name, configPath, logDir string, extraArgs ...string) (*proc, error) {
	logPath := filepath.Join(logDir, name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	args := append([]string{"-c", configPath}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, err
	}
	return &proc{name: name, cmd: cmd, log: logPath}, nil
}

// stop sends SIGTERM and reports how long the process took to exit.
func (p *proc) stop(budget time.Duration) (time.Duration, error) {
	start := time.Now()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return time.Since(start), nil
	case <-time.After(budget):
		_ = p.cmd.Process.Signal(syscall.SIGQUIT)
		time.Sleep(2 * time.Second)
		_ = p.cmd.Process.Kill()
		<-done
		return time.Since(start), fmt.Errorf("%s did not exit within %s", p.name, budget)
	}
}

func (p *proc) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
}

func (p *proc) tail(n int) string {
	b, err := os.ReadFile(p.log)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := ""
	for _, l := range lines {
		out += "      | " + l + "\n"
	}
	return out
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitDialable(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("%s never became dialable", addr)
}

// echoServer bounces every byte back, so a payload checksum proves both
// directions of the tunnel carried the data intact.
type echoServer struct {
	ln    net.Listener
	addr  string
	mu    sync.Mutex
	conns []net.Conn
}

func startEchoServer() (*echoServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	e := &echoServer{ln: ln, addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			e.mu.Lock()
			e.conns = append(e.conns, c)
			e.mu.Unlock()
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return e, nil
}

func (e *echoServer) close() {
	_ = e.ln.Close()
	e.mu.Lock()
	for _, c := range e.conns {
		_ = c.Close()
	}
	e.mu.Unlock()
}

// silentServer accepts and then never reads or writes: the far end of a tunnel
// that is up but idle.
type silentServer struct {
	ln    net.Listener
	addr  string
	mu    sync.Mutex
	conns []net.Conn
}

func startSilentServer() (*silentServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &silentServer{ln: ln, addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
		}
	}()
	return s, nil
}

func (s *silentServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *silentServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
}

func startUDPEcho() (string, func(), error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().String(), func() { _ = pc.Close() }, nil
}
