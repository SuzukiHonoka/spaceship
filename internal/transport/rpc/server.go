package rpc

import (
	"google.golang.org/grpc"
	"io"
	"log"
	"net"
	serverConf "spaceship/internal/config/server"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
	"time"
)

type server struct {
	proxy.UnimplementedProxyServer
	Users map[string]bool
}

func NewServer(users []serverConf.User) *grpc.Server {
	// check users
	if users == nil || len(users) == 0 {
		panic("users can not be empty")
	}
	// create server and register
	s := grpc.NewServer(grpc.ReadBufferSize(0), grpc.WriteBufferSize(0))
	// setup user map
	usersMap := make(map[string]bool, len(users))
	for _, user := range users {
		usersMap[user.UUID.String()] = true
	}
	proxy.RegisterProxyServer(s, &server{Users: usersMap})
	return s
}

func (s *server) Proxy(stream proxy.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	var handshake bool
	// outer connection
	var conn net.Conn
	// block main until canceled
	quit, ack := make(chan interface{}), make(chan bool)
	// read from target and send back to rpc caller
	go func() {
		// only start if ack succeed
		status := <-ack
		if !status {
			log.Println("ack failed")
			return
		}
		// ack ok
		//log.Println("ack ok")
		err := stream.Send(&proxy.ProxyDST{
			Status: proxy.ProxyStatus_Accepted,
			Addr:   conn.LocalAddr().String(),
		})
		if err != nil {
			// free the main
			quit <- struct{}{}
			log.Printf("send target info error: %v", err)
			return
		}
		// buffer
		buf := make([]byte, transport.BufferSize)
		//log.Println("reading from target connection started")
		for {
			select {
			case <-quit:
				buf = nil
				//log.Println("grpc receiver target reader stopped")
				return
			default:
				//log.Println("rpc server: target reading")
				// read reply to buffer
				n, err := conn.Read(buf)
				// if failed
				if err != nil {
					// free the main
					quit <- struct{}{}
					if err != io.EOF {
						log.Printf("read target error: %v", err)
					}
					return
				}
				//log.Println("target read period finish")
				// write back
				//log.Println("rpc server -> client")
				err = stream.Send(&proxy.ProxyDST{
					Status: proxy.ProxyStatus_Session,
					Data:   buf[:n],
					//Addr:   conn.LocalAddr().String(),
				})
				// stop if send rpc failed
				if err != nil {
					// free the main
					quit <- struct{}{}
					log.Printf("send reply to client failed: %v", err)
					return
				}
			}
		}
	}()
	// reading from rpc caller
	go func() {
		var req *proxy.ProxySRC
		var err error
		for {
			select {
			case <-quit:
				log.Println("grpc receiver stopped")
				return
			default:
				//log.Println("rpc server receiving..")
				// receive the request and possible error from the stream object
				req, err = stream.Recv()
				// if there are no more requests, we return
				// handle error from the stream object
				if err != nil {
					// free the main
					quit <- struct{}{}
					if err != io.EOF {
						log.Printf("error when reading client request stream: %v", err)
					}
					return
				}
				// check user
				if _, ok := s.Users[req.Uuid]; !ok {
					// free the main
					quit <- struct{}{}
					log.Printf("unauthticated uuid: %s", req.Uuid)
					return
				}
				//log.Println("authentication accepted")
				// if first ack
				if !handshake {
					//log.Printf("testing if ok: %s:%d", req.Fqdn, req.Port)
					// finally create the dialer
					target := transport.GetTargetDst(req.Fqdn, uint16(req.Port))
					// dial to target with 3 minute timeout
					conn, err = net.DialTimeout("tcp", target, 3*time.Minute)
					// dialer dial failed
					if sendErrorStatusIfError(err, stream) {
						// ack failed
						ack <- false
						// free the main
						quit <- struct{}{}
						log.Println("dialer err")
						return
					}
					//log.Printf("test ok: %s", req.Fqdn)
					// trigger read
					ack <- true
					handshake = true
					log.Println("rpc server proxy received ->", req.Fqdn)
				}
				// after first ack
				if req.Data == nil {
					continue
				}
				//log.Printf("RX: %s", string(data))
				n, err := conn.Write(req.Data)
				if err != nil {
					quit <- struct{}{}
					log.Printf("error when sending client request to target stream: %v", err)
					return
				}
				if n != len(req.Data) {
					quit <- struct{}{}
					log.Println("error when sending client request to target stream: not a full write")
					return
				}
			}
		}
	}()
	<-quit
	err := stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_EOF,
		//Addr:   conn.LocalAddr().String(),
	})
	// stop if send rpc failed
	if err != nil {
		log.Printf("send reply to client failed: %v", err)
	}
	// close target connection
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}
