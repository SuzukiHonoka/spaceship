package socks

import (
	"context"
	"testing"
)

func TestDNSResolver_ResolveLocalhost(t *testing.T) {
	var r DNSResolver
	ctx, ip, err := r.Resolve(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("Resolve(localhost) error = %v", err)
	}
	if ctx == nil {
		t.Fatal("Resolve returned nil context")
	}
	if ip == nil {
		t.Fatal("Resolve returned nil IP")
	}
}

func TestDNSResolver_ResolveInvalid(t *testing.T) {
	var r DNSResolver
	_, _, err := r.Resolve(context.Background(), "this-host-should-not-resolve.invalid")
	if err == nil {
		t.Fatal("Resolve() succeeded for invalid name")
	}
}
