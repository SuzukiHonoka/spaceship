package socks

// StaticCredentials enables using a map directly as a credential store
type StaticCredentials map[string]string

func (s StaticCredentials) Valid(user, password string) bool {
	pass, ok := s[user]
	if !ok {
		return false
	}
	return password == pass
}
