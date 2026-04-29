package http

import "crypto/subtle"

// StaticCredentials enables using a map directly as a credential store
type StaticCredentials map[string]string

func (s StaticCredentials) Valid(user, password []byte) bool {
	pass, ok := s[string(user)]
	if !ok {
		// Always perform a constant-time comparison even for unknown users.
		// Using a zero-filled dummy of the same length as the input ensures
		// that the comparison time is proportional to len(password) in both
		// the "user found" and "user not found" paths, preventing user-existence
		// timing leaks caused by a fixed-length reference string.
		dummy := make([]byte, len(password))
		subtle.ConstantTimeCompare(password, dummy)
		return false
	}
	return subtle.ConstantTimeCompare(password, []byte(pass)) == 1
}
