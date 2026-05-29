package auth

import (
	"testing"
)

func TestStaticCredentials_Valid(t *testing.T) {
	creds := StaticCredentials{
		"user1": "pass1",
		"admin": "supersecret",
		"empty": "",
	}

	tests := []struct {
		name     string
		user     string
		password string
		want     bool
	}{
		{"valid user1", "user1", "pass1", true},
		{"valid admin", "admin", "supersecret", true},
		{"valid empty pass", "empty", "", true},
		{"invalid pass", "user1", "wrong", false},
		{"unknown user", "user2", "pass2", false},
		{"empty user/pass mismatch", "", "", false},
		{"partial user", "user", "pass", false},
		{"partial pass", "admin", "super", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := creds.Valid([]byte(tt.user), []byte(tt.password)); got != tt.want {
				t.Errorf("StaticCredentials.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
