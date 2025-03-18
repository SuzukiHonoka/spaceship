package http

import "crypto/subtle"

// StaticCredentials enables using a map directly as a credential store
type StaticCredentials map[string]string

func (s StaticCredentials) Valid(user, password []byte) bool {
	pass, ok := s[string(user)]
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare(password, []byte(pass)) == 1
}
