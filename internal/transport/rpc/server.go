package rpc

import (
	"fmt"
	"google.golang.org/grpc"
	"io"
	"log"
	"net"
	server2 "spaceship/internal/config/server"
	proxy "spaceship/internal/transport/rpc/proto"
)

type server struct {
	proxy.UnimplementedProxyServer
	Users map[string]bool
}

func NewServer(users []server2.User) *grpc.Server {
	// check users
	if users == nil || len(users) == 0 {
		panic("users can not be empty")
	}
	// create server and register
	s := grpc.NewServer()
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
	// tx/rx interact counter
	counter := 0
	// outer connection
	var conn net.Conn
	// block main until canceled
	quit, ack := make(chan interface{}), make(chan bool)
	// read from target and send back to rpc caller
	go func() {
		// buffer
		buf := make([]byte, 4*1024) // 4K
		// only start if ack succeed
		status := <-ack
		if !status {
			buf = nil
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
			buf = nil
			quit <- struct{}{}
			log.Printf("read target error: %v\n", err)
			return
		}
		log.Println("reading from target connection started")
		for {
			select {
			case <-quit:
				buf = nil
				log.Println("grpc receiver target reader stopped")
				return
			default:
				//log.Println("rpc server: target reading")
				// read reply to buffer
				n, err := conn.Read(buf)
				// if failed
				if err == io.EOF {
					buf = nil
					// free the main
					quit <- struct{}{}
					return
				}
				if err != nil {
					buf = nil
					// free the main
					quit <- struct{}{}
					log.Printf("read target error: %v\n", err)
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
					buf = nil
					// free the main
					quit <- struct{}{}
					log.Printf("send reply to client failed: %v\n", err)
					return
				}
				counter++
			}
		}
	}()
	// reading from rpc caller
	go func() {
		for {
			select {
			case <-quit:
				log.Println("grpc receiver stopped")
				return
			default:
				//log.Println("rpc server receiving..")
				// receive the request and possible error from the stream object
				req, err := stream.Recv()
				// if there are no more requests, we return
				if err == io.EOF {
					// free the main
					quit <- struct{}{}
					return
				}
				// handle error from the stream object
				if err != nil {
					log.Printf("Error when reading client request stream: %v\n", err)
					// free the main
					quit <- struct{}{}
					return
				}
				log.Println("rpc server proxy received ->", req.Fqdn)
				// check user
				if _, ok := s.Users[req.Uuid]; !ok {
					log.Printf("unauthticated uuid: %s", req.Uuid)
					// free the main
					quit <- struct{}{}
					return
				}
				//log.Println("authentication accepted")
				// if first ack
				if counter == 0 {
					//log.Printf("testing if ok: %s:%d\n", req.Fqdn, req.Port)
					// finally create the dialer
					var target string
					if ip := net.ParseIP(req.Fqdn); ip == nil {
						target = fmt.Sprintf("%s:%d", req.Fqdn, req.Port)
					} else {
						if ip.To4() != nil {
							target = fmt.Sprintf("%s:%d", req.Fqdn, req.Port)
						} else {
							target = fmt.Sprintf("[%s]:%d", req.Fqdn, req.Port)
						}
					}
					conn, err = net.Dial("tcp", target)
					// dialer dial failed
					if sendErrorStatusIfError(err, stream) {
						log.Println("dialer err")
						// ack failed
						ack <- false
						// free the main
						quit <- struct{}{}
						return
					}
					//log.Printf("test ok: %s\n", req.Fqdn)
					// trigger read
					ack <- true
					counter++
				}
				// after first ack
				if req.Data == nil {
					continue
				}
				data := req.Data
				//log.Printf("RX: %s", string(data))
				n, err := conn.Write(data)
				if err != nil || n != len(data) {
					log.Printf("Error when sending client request to target stream: %v\n", err)
					<-quit
					return
				}
				counter++
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
		log.Printf("send reply to client failed: %v\n", err)
	}
	return nil
}
