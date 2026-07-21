package socks

import (
	"bytes"
	"testing"
)

func TestAuthenticatorGetCode(t *testing.T) {
	if code := (NoAuthAuthenticator{}).GetCode(); code != NoAuth {
		t.Fatalf("NoAuth GetCode = %d, want %d", code, NoAuth)
	}
	if code := (UserPassAuthenticator{}).GetCode(); code != UserPassAuth {
		t.Fatalf("UserPass GetCode = %d, want %d", code, UserPassAuth)
	}
}

func TestNoAuthAuthenticatorAuthenticate(t *testing.T) {
	var buf bytes.Buffer
	ctx, err := (NoAuthAuthenticator{}).Authenticate(nil, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Method != NoAuth {
		t.Fatalf("Method = %d, want %d", ctx.Method, NoAuth)
	}
	if buf.Len() != 2 {
		t.Fatalf("wrote %d bytes, want 2", buf.Len())
	}
}
