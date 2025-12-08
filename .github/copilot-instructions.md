# Copilot Instructions for Spaceship

## Project Overview

Spaceship is a secure tunnel tool designed to create tunnels to remote networks using gRPC and Protocol Buffers. The project is written in Go and supports multiple platforms (Linux, Windows, Android, Darwin) and architectures (ARM64, AMD64).

## Key Technologies

- **Language**: Go 1.25
- **RPC Framework**: gRPC
- **Serialization**: Protocol Buffers (protobuf)
- **Key Dependencies**:
  - `google.golang.org/grpc` - gRPC framework
  - `google.golang.org/protobuf` - Protocol Buffers
  - `github.com/miekg/dns` - DNS functionality
  - `golang.org/x/net`, `golang.org/x/sync`, `golang.org/x/term` - Go extended libraries

## Project Structure

```
.
├── api/              # Public API interfaces (launcher, stopper, statistics)
├── cmd/spaceship/    # Main application entry point
├── internal/         # Internal packages (not for external use)
│   ├── dns/         # DNS-related functionality
│   ├── http/        # HTTP handling
│   ├── indicator/   # Status indicators
│   ├── router/      # Routing logic
│   ├── socks/       # SOCKS proxy implementation
│   ├── transport/   # Transport layer including gRPC
│   └── utils/       # Utility functions
├── pkg/             # Public packages for external use
│   ├── config/      # Configuration management
│   ├── dns/         # DNS utilities
│   └── logger/      # Logging utilities
└── build.sh         # Cross-platform build script
```

## Development Guidelines

### Code Style

- Follow standard Go conventions and idioms
- Use `gofmt` for code formatting (automatically enforced)
- Run `go vet ./...` before committing changes
- Keep internal packages in `internal/` and only expose public APIs in `pkg/` and `api/`

### Building the Project

```bash
# Install dependencies
go mod tidy -v

# Build for current platform
go build -o spaceship cmd/spaceship/main.go

# Cross-platform builds (creates binaries in build/ directory)
./build.sh cmd/spaceship
```

### Testing

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run specific test file
go test ./internal/utils/extractDomain_test.go
```

### Linting

```bash
# Run Go vet (official Go static analyzer)
go vet ./...

# Check for common issues
go fmt ./...
```

### Protocol Buffers

- Proto files are located in `internal/transport/rpc/proto/`
- The main proto file is `proxy.proto`
- When modifying proto files, regenerate Go code using `protoc`

### Configuration

- Configuration files use JSON format (see `config.json` pattern)
- Config files are gitignored to prevent accidental commits of sensitive data
- Configuration handling is in `pkg/config/`

### Security Considerations

- The application currently uses insecure gRPC connections
- Production deployments should use a reverse proxy with TLS (e.g., Nginx + TLS)
- Never commit sensitive configuration files or credentials
- This tool is for personal/authorized use only - illegal usage is strictly prohibited

### Making Changes

1. **Minimize changes**: Make the smallest possible modifications to achieve the goal
2. **Test your changes**: Run tests and vet before committing
3. **Update documentation**: If your changes affect public APIs or usage, update relevant docs
4. **Follow existing patterns**: Match the coding style and patterns already in the codebase
5. **Internal vs Public**: Keep implementation details in `internal/`, only expose stable APIs in `pkg/` and `api/`

### Common Tasks

#### Adding a new feature
1. Determine if it belongs in `internal/`, `pkg/`, or `api/`
2. Implement with tests when possible
3. Run `go vet ./...` and `go test ./...`
4. Update relevant documentation

#### Modifying gRPC services
1. Update `.proto` files in `internal/transport/rpc/proto/`
2. Regenerate Go code with protoc
3. Update client/server implementations
4. Test the changes thoroughly

#### Updating dependencies
1. Modify `go.mod` or use `go get`
2. Run `go mod tidy`
3. Test to ensure compatibility
4. Check for security vulnerabilities in new dependencies

### Build and CI/CD

- GitHub Actions workflow is defined in `.github/workflows/build.yml`
- Builds are triggered on release publication
- The workflow:
  1. Sets up Go 1.25
  2. Runs `go mod tidy`
  3. Runs `go vet ./...`
  4. Executes the build script
  5. Uploads artifacts and creates GitHub releases

### Ignored Files

The following are gitignored and should not be committed:
- `.idea/`, `.vscode/` - IDE files
- `test/` - Test outputs
- `vendor/` - Vendored dependencies
- `*.json` - Configuration files (may contain sensitive data)
- `build/` - Build artifacts

## Additional Resources

- [gRPC Documentation](https://grpc.io/docs/)
- [Protocol Buffers Guide](https://protobuf.dev/)
- [Go Documentation](https://go.dev/doc/)

## Legal Notice

This program is for personal/authorized use only. Users must:
- Adhere to laws of their respective countries
- Not use the program for illegal purposes
- Not share the program with unauthorized parties
- Accept that the program is provided "as is" without warranties
