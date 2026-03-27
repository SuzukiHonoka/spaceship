package rpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MetadataKeyUserID is the gRPC metadata key for user authentication.
// Sent via HTTP/2 HPACK headers — compressed after first use per connection.
const MetadataKeyUserID = "x-user-id"

type userIDContextKey struct{}

// UserIDFromContext retrieves the validated user ID from context.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDContextKey{}).(string)
	return id, ok
}

// --- Client interceptors ---

// UnaryClientAuthInterceptor returns a unary interceptor that attaches
// the user UUID to outgoing gRPC metadata on every call.
func UnaryClientAuthInterceptor(getUUID func() string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, MetadataKeyUserID, getUUID())
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamClientAuthInterceptor returns a stream interceptor that attaches
// the user UUID to outgoing gRPC metadata on every stream.
func StreamClientAuthInterceptor(getUUID func() string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = metadata.AppendToOutgoingContext(ctx, MetadataKeyUserID, getUUID())
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// --- Server interceptors ---

// UnaryServerAuthInterceptor returns a unary interceptor that extracts and
// validates the user UUID from incoming gRPC metadata before the handler runs.
func UnaryServerAuthInterceptor(matchUser func(string) bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		uid, err := extractAndValidateUserID(ctx, matchUser)
		if err != nil {
			return nil, err
		}
		return handler(context.WithValue(ctx, userIDContextKey{}, uid), req)
	}
}

// StreamServerAuthInterceptor returns a stream interceptor that extracts and
// validates the user UUID from incoming gRPC metadata before the handler runs.
func StreamServerAuthInterceptor(matchUser func(string) bool) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		uid, err := extractAndValidateUserID(ss.Context(), matchUser)
		if err != nil {
			return err
		}
		return handler(srv, &authenticatedServerStream{
			ServerStream: ss,
			ctx:          context.WithValue(ss.Context(), userIDContextKey{}, uid),
		})
	}
}

func extractAndValidateUserID(ctx context.Context, matchUser func(string) bool) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get(MetadataKeyUserID)
	if len(values) == 0 || values[0] == "" {
		return "", status.Error(codes.Unauthenticated, "missing user id")
	}
	uid := values[0]
	if !matchUser(uid) {
		return "", status.Errorf(codes.Unauthenticated, "unknown user: %s", uid)
	}
	return uid, nil
}

// authenticatedServerStream wraps grpc.ServerStream to override Context()
// with one that carries the validated user ID.
type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedServerStream) Context() context.Context {
	return s.ctx
}
