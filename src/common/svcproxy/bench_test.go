package svcproxy

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// BenchmarkUnary compares a unary call through listener + Unix socket with
// the same call to a plain mTLS server on TCP (today's topology). Run:
//
//	go test ./common/svcproxy/ -run xxx -bench Unary -benchmem
func BenchmarkUnary(b *testing.B) {
	for _, size := range []int{1 << 10, 256 << 10} {
		payload := wrapperspb.String(strings.Repeat("x", size))
		name := fmt.Sprintf("%dKiB", size>>10)

		b.Run("via-listener/"+name, func(b *testing.B) {
			s := startStack(b, nil)
			benchUnary(b, s.client(b, "bench", nil), payload)
		})

		b.Run("direct-mtls/"+name, func(b *testing.B) {
			s := startStack(b, nil) // reused for its CA
			ctx, cancel := context.WithCancel(context.Background())
			b.Cleanup(cancel)
			serverCerts := s.ca.CertsDir(b, s.ca.Issue(b, "direct"))
			port := freePort(b)
			go connection.StartServer(ctx, discard(), port, serverCerts, nil, func(g *grpc.Server) {
				g.RegisterService(&echoDesc, struct{}{})
			})
			clientCerts := s.ca.CertsDir(b, s.ca.Issue(b, "bench"))
			var conn *grpc.ClientConn
			require.Eventually(b, func() bool {
				c, err := connection.Connect("localhost", port, 2, clientCerts)
				conn = c
				return err == nil
			}, 10*time.Second, 50*time.Millisecond)
			b.Cleanup(func() { conn.Close() })
			benchUnary(b, conn, payload)
		})
	}
}

func benchUnary(b *testing.B, conn *grpc.ClientConn, payload *wrapperspb.StringValue) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload.Value)))
	var out wrapperspb.StringValue
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := conn.Invoke(context.Background(), "/test.Echo/Echo", payload, &out); err != nil {
			b.Fatal(err)
		}
	}
}
