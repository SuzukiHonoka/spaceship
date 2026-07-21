package socks

import "testing"

func TestPermitAllAndNone(t *testing.T) {
	all := PermitAll()
	none := PermitNone()

	cases := []struct {
		cmd  uint8
		name string
	}{
		{ConnectCommand, "connect"},
		{BindCommand, "bind"},
		{AssociateCommand, "associate"},
		{99, "unknown"},
	}
	for _, tc := range cases {
		req := &Request{Command: tc.cmd}
		if !all.Allow(req) && tc.cmd != 99 {
			t.Errorf("PermitAll denied %s", tc.name)
		}
		if all.Allow(req) && tc.cmd == 99 {
			t.Errorf("PermitAll allowed unknown command")
		}
		if none.Allow(req) {
			t.Errorf("PermitNone allowed %s", tc.name)
		}
	}
}

func TestPermitCommandSelective(t *testing.T) {
	p := &PermitCommand{EnableConnect: true, EnableBind: false, EnableAssociate: true}
	if !p.Allow(&Request{Command: ConnectCommand}) {
		t.Error("connect should be allowed")
	}
	if p.Allow(&Request{Command: BindCommand}) {
		t.Error("bind should be denied")
	}
	if !p.Allow(&Request{Command: AssociateCommand}) {
		t.Error("associate should be allowed")
	}
}
