package rpc

import (
	"log"
	proxy "spaceship/internal/transport/rpc/proto"
)

func sendErrorStatusIfError(err error, stream proxy.Proxy_ProxyServer) bool {
	if err == nil {
		return false
	}
	log.Printf("Error: %v", err)
	err = stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_Error,
	})
	if err != nil {
		log.Printf("send rpc message failed: %v", err)
	}
	return true
}
