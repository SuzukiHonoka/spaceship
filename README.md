# Spaceship

Spaceship is a tool designed to create secure tunnels to remote networks.

# Technologies Used

- gRPC
- Protocol Buffers (protobuf)

## Usage

```shell
# spaceship -h
Usage of spaceship:
  -c string
        config path (default "./config.json")
  -interval duration
        show stats interval in seconds (default 1s)
  -s    show stats
  -v    show spaceship version
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

Spaceship currently uses pure gRPC with the insecure option. For secure communication, it is highly recommended to set
up a reverse proxy with TLS, such as `Nginx + TLS`.

## Development Status

The program is still under development. Contributions via pull requests are greatly appreciated.

## Legal Disclaimer

This program is provided "as is," with no warranties or guarantees. It is available only to repository members, and
sharing it with others is strictly prohibited. Users must adhere to the laws of their respective countries. **Any
illegal use of this program is strictly prohibited.**