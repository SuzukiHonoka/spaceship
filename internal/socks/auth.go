package socks

import (
	"fmt"
	"io"
)

const (
	NoAuth          = uint8(0)
	noAcceptable    = uint8(255)
	UserPassAuth    = uint8(2)
	userAuthVersion = uint8(1)
	authSuccess     = uint8(0)
	authFailure     = uint8(1)
)

var (
	ErrUserAuthFailed  = fmt.Errorf("user authentication failed")
	ErrNoSupportedAuth = fmt.Errorf("no supported authentication mechanism")
)

// AuthContext A Request encapsulates authentication state provided during negotiation
type AuthContext struct {
	// Provided auth method
	Method uint8
	// Payload provided during negotiation.
	// Keys depend on the used auth method.
	// For UserPass-auth contains Username
	Payload map[string]string
}

type Authenticator interface {
	Authenticate(reader io.Reader, writer io.Writer) (*AuthContext, error)
	GetCode() uint8
}

// NoAuthAuthenticator is used to handle the "No Authentication" mode
type NoAuthAuthenticator struct{}

func (a NoAuthAuthenticator) GetCode() uint8 {
	return NoAuth
}

func (a NoAuthAuthenticator) Authenticate(_ io.Reader, writer io.Writer) (*AuthContext, error) {
	_, err := writer.Write([]byte{socks5Version, NoAuth})
	return &AuthContext{NoAuth, nil}, err
}

// UserPassAuthenticator is used to handle username/password based authentication
type UserPassAuthenticator struct {
	Credentials StaticCredentials
}

func (a UserPassAuthenticator) GetCode() uint8 {
	return UserPassAuth
}

func (a UserPassAuthenticator) Authenticate(reader io.Reader, writer io.Writer) (*AuthContext, error) {
	// Tell the client to use user/pass auth
	if _, err := writer.Write([]byte{socks5Version, UserPassAuth}); err != nil {
		return nil, err
	}

	// Get the version and username length
	header := []byte{0, 0}
	if _, err := io.ReadAtLeast(reader, header, 2); err != nil {
		return nil, err
	}

	// Ensure we are compatible
	if header[0] != userAuthVersion {
		return nil, fmt.Errorf("unsupported auth version: %v", header[0])
	}

	// Get the username
	userLen := int(header[1])
	user := make([]byte, userLen)
	if _, err := io.ReadAtLeast(reader, user, userLen); err != nil {
		return nil, err
	}

	// Get the password length
	if _, err := reader.Read(header[:1]); err != nil {
		return nil, err
	}

	// Get the password
	passLen := int(header[0])
	pass := make([]byte, passLen)
	if _, err := io.ReadAtLeast(reader, pass, passLen); err != nil {
		return nil, err
	}

	// Verify the password
	if a.Credentials.Valid(user, pass) {
		if _, err := writer.Write([]byte{userAuthVersion, authSuccess}); err != nil {
			return nil, err
		}
	} else {
		if _, err := writer.Write([]byte{userAuthVersion, authFailure}); err != nil {
			return nil, err
		}
		return nil, ErrUserAuthFailed
	}

	// Done
	return &AuthContext{UserPassAuth, map[string]string{"Username": string(user)}}, nil
}

// authenticate is used to handle connection authentication
func (s *Server) authenticate(conn io.Writer, bufConn io.Reader) (*AuthContext, error) {
	// get the client supported methods
	methods, err := readMethods(bufConn)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth methods: %v", err)
	}
	// if credentials not exist
	if len(s.config.Credentials) == 0 {
		return NoAuthAuthenticator{}.Authenticate(bufConn, conn)
	}
	// check client whether supported user-password auth
	for _, method := range methods {
		if method == UserPassAuth {
			return UserPassAuthenticator{s.config.Credentials}.Authenticate(bufConn, conn)
		}
	}
	// no usable method found
	return nil, noAcceptableAuth(conn)
}

// noAcceptableAuth is used to handle when we have no eligible
// authentication mechanism
func noAcceptableAuth(conn io.Writer) error {
	_, _ = conn.Write([]byte{socks5Version, noAcceptable})
	return ErrNoSupportedAuth
}

// readMethods is used to read the number of methods and proceeding auth methods
func readMethods(r io.Reader) ([]byte, error) {
	// methods len
	header := []byte{0}
	if _, err := r.Read(header); err != nil {
		return nil, err
	}
	numMethods := int(header[0])
	methods := make([]byte, numMethods)
	_, err := io.ReadAtLeast(r, methods, numMethods)
	return methods, err
}
