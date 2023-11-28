# Spaceship
A program helps you to create a tunnel to the remote network.

## Technique
 - grpc
 - protobuf

## Usage
```shell
root@starx:/opt/spaceship# ./spaceship -h
spaceship v1.x.x for personal use only, absolutely without any warranty, any kind of illegal intention by using this program are strongly forbidden.
Usage of spaceship:
  -c string
        config path (default "./config.json")
```

## Nginx Reserve Proxy Configuration
```nginx
...
    location /proxy. {
        grpc_intercept_errors on;
        grpc_socket_keepalive on;
        grpc_send_timeout 3600s;
        grpc_read_timeout 3600s;
        grpc_pass grpc://127.0.0.1:12345;
    }
...
```
Note that `proxy` is the current proto source package name

## Safety
Since it only uses pure grpc with insecure option for now, you should really set up a reserve proxy with TLS for real 
transfer. Like `Nginx + TLS`.

## Status
The program is still under developing, any PR are warmly welcomed.

## TODO
- support doh/dot as dns resolver

## Legal statement
This program is provided as it is, absolutely without any warranty, and it's only available for the repo members, 
sharing it to any other one is strongly forbidden.  
Also, while you are using the program, you must obey the laws in your current living country.  
**ANY KIND OF ILLEGAL INTENTION BY USING THIS PROGRAM ARE STRONGLY FORBIDDEN**
