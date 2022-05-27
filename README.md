# Spaceship
A program helps you to create a tunnel to the remote network.

## Technique
 - grpc
 - protobuf

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
Also, while you using the program, you must obey the laws in your current living country.  
**ANY ILLEGAL MADE BY USING THIS PROGRAM ARE ON YOUR OWN**