package auth

import (
	"crypto/sha256"
	"crypto/subtle"
)

var invalidCredentialDigest = sha256.Sum256([]byte("spaceship-invalid-credential"))

// StaticCredentials enables using a map directly as a credential store.
type StaticCredentials map[string]string

// Valid reports whether the given user/password pair matches a stored entry.
// Constant-time comparison is used in both the "user found" and "user not found"
// paths to prevent timing-based user-existence leaks.
func (s StaticCredentials) Valid(user, password []byte) bool {
	pass, ok := s[string(user)]
	passwordDigest := sha256.Sum256(password)
	storedDigest := sha256.Sum256([]byte(pass))
	if !ok {
		storedDigest = invalidCredentialDigest
	}

	// Digests have a fixed length, so wrong-length passwords and unknown users
	// take the same constant-time comparison path.
	matched := subtle.ConstantTimeCompare(passwordDigest[:], storedDigest[:]) == 1
	return ok && matched
}
