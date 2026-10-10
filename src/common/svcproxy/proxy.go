// Package svcproxy is the listener's transparent gRPC proxy: it routes each
// call, by the service named in its method path, to the backend listening on
// <runDir>/services/<Service>.sock, forwarding the caller's verified
// certificate chain. It never decodes a message, so unary and streaming RPCs
// of any service pass through and new services need no change here.
package svcproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Proxy forwards calls to local Unix-socket backends.
type Proxy struct {
	logger *slog.Logger
	runDir string

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn // by socket path
}

// New returns a Proxy resolving backends under runDir (must be absolute).
func New(logger *slog.Logger, runDir string) *Proxy {
	return &Proxy{logger: logger, runDir: runDir, conns: map[string]*grpc.ClientConn{}}
}

// ServerOptions are the grpc.ServerOptions that make a server hand every call
// -- whatever service it names -- to this proxy, undecoded.
func (p *Proxy) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ForceServerCodecV2(rawCodec{}),
		grpc.UnknownServiceHandler(p.handle),
	}
}

// Close releases the cached backend connections.
func (p *Proxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for path, c := range p.conns {
		c.Close()
		delete(p.conns, path)
	}
}

// ServiceFromMethod extracts the fully qualified service name from a gRPC
// full method ("/pkg.Service/Method"), rejecting anything that could not be a
// socket file name.
func ServiceFromMethod(fullMethod string) (string, error) {
	rest, ok := strings.CutPrefix(fullMethod, "/")
	if ok {
		service, method, found := strings.Cut(rest, "/")
		if found && method != "" && connection.ValidServiceName(service) {
			return service, nil
		}
	}
	return "", fmt.Errorf("malformed method %q", fullMethod)
}

func (p *Proxy) conn(sock string) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.conns[sock]; ok {
		return c, nil
	}
	c, err := grpc.NewClient("unix://"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// A local socket: reconnect fast after a backend restart instead of
		// grpc's default backoff, which grows to two minutes.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 100 * time.Millisecond, Multiplier: 1.6, MaxDelay: 2 * time.Second},
			MinConnectTimeout: time.Second,
		}),
	)
	if err != nil {
		return nil, err
	}
	p.conns[sock] = c
	return c, nil
}

func (p *Proxy) handle(_ any, serverStream grpc.ServerStream) (err error) {
	start := time.Now()
	ctx := serverStream.Context()
	fullMethod, _ := grpc.MethodFromServerStream(serverStream)
	caller, _ := mtls.PeerHostname(ctx)
	defer func() {
		code := status.Code(err)
		attrs := []any{"caller", caller, "method", fullMethod, "code", code.String(), "duration", time.Since(start)}
		if code == codes.OK {
			p.logger.Debug("proxied call", attrs...)
		} else {
			p.logger.Warn("proxied call failed", attrs...)
		}
	}()

	service, err := ServiceFromMethod(fullMethod)
	if err != nil {
		return status.Error(codes.Unimplemented, err.Error())
	}
	sock, err := connection.ServiceSocketPath(p.runDir, service)
	if err != nil {
		return status.Error(codes.Unimplemented, err.Error())
	}
	if _, statErr := os.Stat(sock); statErr != nil {
		return status.Errorf(codes.Unimplemented, "service %s is not served on this host", service)
	}
	chain, err := mtls.PeerChainDER(ctx)
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "no verified client certificate: %v", err)
	}
	backend, err := p.conn(sock)
	if err != nil {
		return status.Errorf(codes.Unavailable, "service %s: %v", service, err)
	}

	md, _ := metadata.FromIncomingContext(ctx)
	outCtx, cancel := context.WithCancel(metadata.NewOutgoingContext(ctx, mtls.WithForwardedChain(md, chain)))
	defer cancel()

	clientStream, err := grpc.NewClientStream(outCtx,
		&grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, backend, fullMethod,
		grpc.ForceCodecV2(rawCodec{}))
	if err != nil {
		if status.Code(err) == codes.Unavailable {
			return status.Errorf(codes.Unavailable, "service %s is not accepting connections: %v", service, err)
		}
		return err
	}

	s2c := forwardServerToClient(serverStream, clientStream)
	c2s := forwardClientToServer(clientStream, serverStream)
	for i := 0; i < 2; i++ {
		select {
		case e := <-s2c:
			if e == io.EOF {
				clientStream.CloseSend()
			} else {
				cancel()
				return status.Errorf(codes.Internal, "proxying client to backend: %v", e)
			}
		case e := <-c2s:
			serverStream.SetTrailer(clientStream.Trailer())
			if e != io.EOF {
				return e // the backend's own status, passed through
			}
			return nil
		}
	}
	return status.Error(codes.Internal, "proxy loop ended unexpectedly")
}

func forwardServerToClient(src grpc.ServerStream, dst grpc.ClientStream) chan error {
	done := make(chan error, 1)
	go func() {
		f := &frame{}
		for {
			if err := src.RecvMsg(f); err != nil {
				done <- err
				return
			}
			if err := dst.SendMsg(f); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}

func forwardClientToServer(src grpc.ClientStream, dst grpc.ServerStream) chan error {
	done := make(chan error, 1)
	go func() {
		f := &frame{}
		for i := 0; ; i++ {
			if err := src.RecvMsg(f); err != nil {
				done <- err
				return
			}
			if i == 0 {
				md, err := src.Header()
				if err != nil {
					done <- err
					return
				}
				if err := dst.SendHeader(md); err != nil {
					done <- err
					return
				}
			}
			if err := dst.SendMsg(f); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}
