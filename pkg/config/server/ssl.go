package server

type SSL struct {
	PublicKey  string `json:"cert"` // certificate path
	PrivateKey string `json:"key"`  // certificate key path
}
