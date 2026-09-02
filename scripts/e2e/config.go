//go:build unix

package main

import (
	"encoding/json"
	"os"
)

func listenAddrFromConfig(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", err
	}
	return cfg.Listen, nil
}
