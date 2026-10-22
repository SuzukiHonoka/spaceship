package api

import "github.com/SuzukiHonoka/spaceship/v2/internal/transport"

// SetSocketProtector installs a process-wide hook for every outbound socket
// Spaceship creates, including when the bypass mark is 0. Call it before
// Launch. A nil hook disables protection.
//
// Android apps that do not exclude their own UID pass a function that calls
// VpnService.protect(fd). A protector error fails that dial. Spaceship does
// not import Android APIs; the embedder injects the callback.
func SetSocketProtector(fn func(fd int) error) {
	transport.SetSocketProtector(fn)
}
