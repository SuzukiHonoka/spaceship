package router

import (
	"os"
	"testing"
)

func TestRoute_GenerateCache_Exact(t *testing.T) {
	r := &Route{
		Sources:   []string{"example.com", "google.com"},
		MatchType: TypeExact,
	}
	if err := r.GenerateCache(); err != nil {
		t.Fatalf("GenerateCache() error = %v", err)
	}
	if len(r.cache.ExactMap) != 2 {
		t.Errorf("ExactMap size = %d, want 2", len(r.cache.ExactMap))
	}
	if _, ok := r.cache.ExactMap["example.com"]; !ok {
		t.Errorf("example.com not in ExactMap")
	}
}

func TestRoute_GenerateCache_CIDR(t *testing.T) {
	r := &Route{
		Sources:   []string{"127.0.0.0/8", "192.168.1.1/32"},
		MatchType: TypeCIDR,
	}
	if err := r.GenerateCache(); err != nil {
		t.Fatalf("GenerateCache() error = %v", err)
	}
	if len(r.cache.CIDRList) != 2 {
		t.Errorf("CIDRList size = %d, want 2", len(r.cache.CIDRList))
	}
}

func TestRoute_GenerateCache_Regex(t *testing.T) {
	r := &Route{
		Sources:   []string{`^.*\.google\.com$`, `^example\..*$`},
		MatchType: TypeRegex,
	}
	if err := r.GenerateCache(); err != nil {
		t.Fatalf("GenerateCache() error = %v", err)
	}
	if len(r.cache.RegexpList) != 2 {
		t.Errorf("RegexpList size = %d, want 2", len(r.cache.RegexpList))
	}
}

func TestRoute_GenerateCache_ExtFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "route-ext")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString("file.example.com\nanother.com\n"); err != nil {
		t.Fatal(err)
	}
	tmpfile.Close()

	r := &Route{
		Sources:   []string{"manual.com"},
		Ext:       tmpfile.Name(),
		MatchType: TypeExact,
	}
	if err := r.GenerateCache(); err != nil {
		t.Fatalf("GenerateCache() error = %v", err)
	}
	if len(r.cache.ExactMap) != 3 {
		t.Errorf("ExactMap size = %d, want 3 (manual + 2 from file)", len(r.cache.ExactMap))
	}
}

func TestRoute_Match_Exact(t *testing.T) {
	r := &Route{
		Sources:   []string{"example.com"},
		MatchType: TypeExact,
	}
	r.GenerateCache()

	if !r.Match("example.com") {
		t.Errorf("expected match for example.com")
	}
	if r.Match("sub.example.com") {
		t.Errorf("did not expect match for sub.example.com")
	}
}

func TestRoute_Match_Domain(t *testing.T) {
	r := &Route{
		Sources:   []string{"google.com"},
		MatchType: TypeDomain,
	}
	r.GenerateCache()

	if !r.Match("google.com") {
		t.Errorf("expected match for google.com")
	}
	if !r.Match("www.google.com") {
		t.Errorf("expected match for www.google.com")
	}
}
