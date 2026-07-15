package utils

import "testing"

func TestExtractDomain(t *testing.T) {
	domains := map[string]string{
		"google.com":         "google.com",
		"www.google.com":     "google.com",
		"sub.www.google.com": "google.com",
		"google.co.jp":       "google.co.jp",
		"www.google.co.jp":   "google.co.jp",
		"http.debian.net":    "debian.net",
		"debian.net":         "debian.net",
		// normalization: case + trailing FQDN dot
		"WWW.Google.COM":  "google.com",
		"www.google.com.": "google.com",
		"Google.COM.":     "google.com",
	}
	for k, v := range domains {
		domain := ExtractDomain(k)
		if domain != v {
			t.Errorf("domain: %s extracted: %s should be: %s", k, domain, v)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := map[string]string{
		"Example.COM":   "example.com",
		"example.com.":  "example.com",
		" Example.COM.": "example.com",
		"":              "",
		"  ":            "",
	}
	for in, want := range tests {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}
