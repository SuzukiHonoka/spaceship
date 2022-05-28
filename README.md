# Spaceship
A program helps you to create a tunnel to the remote network.

## Technique
 - grpc
 - protobuf

## Usage
```shell
root@starx:/opt/spaceship# ./spaceship -h
2022/05/27 21:53:04 spaceship v1.0
Usage of ./spaceship:
  -c string
    	config path (default "./config.json")
```
The spaceship server and client configuration example are now added in repo/example/config

## Nginx Reserve Proxy Configuration
```nginx
...
    location /proxy. {  
        grpc_read_timeout 3600s;
        grpc_pass grpc://127.0.0.1:12345;
    }
...
```
* `proxy` is the current proto source package name

## Safety
Since it only uses pure grpc with insecure option for now, you should really set up a reserve proxy with TLS for real 
transfer. Like `Nginx + TLS`.

## Status
The program is still under developing, any PR are warmly welcomed.

## TODO
- support doh/dot as dns resolver
- support traffic routing
- support http for client inbound
- integrate common transfer interface 

## Legal statement
This program now only available for the repo members, sharing it to any other one is strongly forbidden.  
Also, while you are using the program, you must obey the laws in your current living country.  
**ANY ILLEGAL ACTION MADE BY USING THIS PROGRAM ARE ON YOUR OWN**