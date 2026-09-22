//go:build unix && !linux

package main

func runTUNChildIfRequested(string, string) bool { return false }

func runTUNSuite(string, string) {
	skip("tun/device lifecycle", "linux only")
}
