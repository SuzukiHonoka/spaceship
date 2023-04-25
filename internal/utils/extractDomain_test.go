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
	}
	for k, v := range domains {
		domain := ExtractDomain(k)
		if domain != v {
			t.Errorf("domain: %s extracted: %s should be: %s", k, domain, v)
		}
	}
}
