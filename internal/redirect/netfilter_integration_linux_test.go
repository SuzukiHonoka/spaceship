//go:build linux

package redirect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

const (
	redirectIntegrationEnv       = "SPACESHIP_REDIRECT_INTEGRATION"
	redirectIntegrationIPv6Env   = "SPACESHIP_REDIRECT_INTEGRATION_IPV6"
	redirectIntegrationHelperEnv = "SPACESHIP_REDIRECT_INTEGRATION_HELPER"
)

// TestNetfilterRedirectIntegration exercises the real kernel conntrack and
// SO_ORIGINAL_DST path in a disposable network namespace. It is opt-in because
// it requires util-linux unshare, iproute2, and iptables with namespace
// privileges.
func TestNetfilterRedirectIntegration(t *testing.T) {
	if os.Getenv(redirectIntegrationHelperEnv) == "1" {
		runNetfilterIntegrationHelper(t)
		return
	}
	if os.Getenv(redirectIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the isolated netfilter integration test", redirectIntegrationEnv)
	}

	required := []string{"unshare", "ip", "iptables"}
	if os.Getenv(redirectIntegrationIPv6Env) == "1" {
		required = append(required, "ip6tables")
	}
	for _, name := range required {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required integration tool %q not found: %v", name, err)
		}
	}

	args := []string{"--net", "--mount", "--mount-proc", "--fork"}
	if os.Geteuid() != 0 {
		args = append([]string{"--user", "--map-root-user"}, args...)
	}
	args = append(args, "--", os.Args[0], "-test.run=^TestNetfilterRedirectIntegration$", "-test.count=1", "-test.v")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", args...)
	cmd.Env = append(os.Environ(), redirectIntegrationHelperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("isolated netfilter test timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("isolated netfilter test failed: %v\n%s", err, output)
	}
	t.Logf("isolated netfilter test output:\n%s", output)
}

func runNetfilterIntegrationHelper(t *testing.T) {
	runIntegrationCommand(t, "ip", "link", "set", "lo", "up")
	t.Run("IPv4", func(t *testing.T) {
		runNetfilterRedirectFlow(t, "tcp4", "127.0.0.1", "tcp4", "127.0.0.1", "iptables")
	})
	t.Run("IPv4 through dual-stack listener", func(t *testing.T) {
		runNetfilterRedirectFlow(t, "tcp", "::", "tcp4", "127.0.0.1", "iptables")
	})
	if os.Getenv(redirectIntegrationIPv6Env) == "1" {
		t.Run("IPv6", func(t *testing.T) {
			runNetfilterRedirectFlow(t, "tcp6", "::1", "tcp6", "::1", "ip6tables")
		})
	}
}

type integrationEchoTransport struct {
	target chan string
}

func (t *integrationEchoTransport) String() string {
	return "integration-echo"
}

func (t *integrationEchoTransport) Proxy(
	_ context.Context,
	addr string,
	localAddr chan<- string,
	dst io.Writer,
	src io.Reader,
) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	t.target <- addr

	payload := make([]byte, 4)
	if _, err := io.ReadFull(src, payload); err != nil {
		return err
	}
	_, err := dst.Write(bytes.ToUpper(payload))
	return err
}

func (t *integrationEchoTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (t *integrationEchoTransport) Close() error {
	return nil
}

func runNetfilterRedirectFlow(
	t *testing.T,
	listenNetwork string,
	listenHost string,
	flowNetwork string,
	targetHost string,
	iptables string,
) {
	t.Helper()

	listener, err := net.Listen(listenNetwork, net.JoinHostPort(listenHost, "0"))
	if err != nil {
		t.Fatalf("%s redirect listen: %v", listenNetwork, err)
	}
	redirectPort := listener.Addr().(*net.TCPAddr).Port

	targetProbe, err := net.Listen(flowNetwork, net.JoinHostPort(targetHost, "0"))
	if err != nil {
		_ = listener.Close()
		t.Fatalf("%s target port probe: %v", flowNetwork, err)
	}
	target := targetProbe.Addr().(*net.TCPAddr)
	if err := targetProbe.Close(); err != nil {
		_ = listener.Close()
		t.Fatalf("%s target port probe close: %v", flowNetwork, err)
	}

	rule := []string{
		"-w", "-t", "nat", "-A", "OUTPUT",
		"-p", "tcp", "-d", targetHost, "--dport", strconv.Itoa(target.Port),
		"-j", "REDIRECT", "--to-ports", strconv.Itoa(redirectPort),
	}
	runIntegrationCommand(t, iptables, rule...)
	t.Cleanup(func() {
		deleteRule := append([]string(nil), rule...)
		deleteRule[3] = "-D"
		_ = exec.Command(iptables, deleteRule...).Run()
	})

	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, &Config{MaxConnections: 4})
	if err != nil {
		cancel()
		_ = listener.Close()
		t.Fatal(err)
	}
	tr := &integrationEchoTransport{target: make(chan string, 1)}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return tr, nil
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(listener)
	}()

	client, err := net.DialTimeout(flowNetwork, target.String(), 3*time.Second)
	if err != nil {
		cancel()
		<-serveErr
		t.Fatalf("%s redirected dial: %v", flowNetwork, err)
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		_ = client.Close()
		cancel()
		<-serveErr
		t.Fatalf("%s redirected write: %v", flowNetwork, err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(client, reply); err != nil {
		_ = client.Close()
		cancel()
		<-serveErr
		t.Fatalf("%s redirected read: %v", flowNetwork, err)
	}
	_ = client.Close()
	if got := string(reply); got != "PING" {
		cancel()
		<-serveErr
		t.Fatalf("%s redirected reply = %q, want PING", flowNetwork, got)
	}

	select {
	case got := <-tr.target:
		if got != target.String() {
			cancel()
			<-serveErr
			t.Fatalf("%s original destination = %q, want %q", flowNetwork, got, target)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-serveErr
		t.Fatalf("%s route did not receive the original destination", flowNetwork)
	}

	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s server shutdown error = %v, want context.Canceled", flowNetwork, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s redirect server did not stop", flowNetwork)
	}
}

func runIntegrationCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
	}
}

var _ transport.Transport = (*integrationEchoTransport)(nil)
