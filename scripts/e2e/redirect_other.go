//go:build unix && !linux

package main

// Transparent REDIRECT is a Linux netfilter feature; there is nothing to drive
// on other unix platforms.
func runRedirectChildIfRequested(string, string) bool { return false }

func runRedirectSuite(string, string) {
	skip("redirect/transparent capture", "linux only")
}
