// src/cmd/api-server/loki_tail_test.go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPLokiTailer_DeliversMessagesViaOnMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var gotQuery string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.WriteJSON(map[string]any{
			"streams": []map[string]any{
				{"stream": map[string]string{"hostname": "webserver"}, "values": [][]string{{"1752400500000000000", "line1"}}},
			},
		}))
	}))
	defer lokiStub.Close()

	wsBase := "ws" + strings.TrimPrefix(lokiStub.URL, "http")
	tailer := newHTTPLokiTailer(wsBase, websocket.DefaultDialer)

	received := make(chan lokiTailMessage, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tailer.Tail(ctx, `{binary=~"agent"}`, time.Unix(1752400000, 0), func(msg lokiTailMessage) error {
		received <- msg
		cancel() // one message is enough for this test
		return nil
	})

	select {
	case msg := <-received:
		require.Len(t, msg.Streams, 1)
		assert.Equal(t, "webserver", msg.Streams[0].Stream["hostname"])
		require.Len(t, msg.Streams[0].Values, 1)
		assert.Equal(t, "line1", msg.Streams[0].Values[0].Line)
	case <-time.After(2 * time.Second):
		t.Fatal("no message received")
	}
	assert.Contains(t, gotQuery, "query=")
	assert.Contains(t, gotQuery, "start=1752400000000000000")
}

func TestHTTPLokiTailer_DialFailureReturnsError(t *testing.T) {
	tailer := newHTTPLokiTailer("ws://127.0.0.1:1", websocket.DefaultDialer) // nothing listens here
	err := tailer.Tail(context.Background(), `{}`, time.Now(), func(lokiTailMessage) error { return nil })
	assert.Error(t, err)
}

func TestHTTPLokiTailer_OnMessageErrorStopsTail(t *testing.T) {
	upgrader := websocket.Upgrader{}
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for i := 0; i < 5; i++ {
			if err := conn.WriteJSON(map[string]any{"streams": []map[string]any{}}); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer lokiStub.Close()

	wsBase := "ws" + strings.TrimPrefix(lokiStub.URL, "http")
	tailer := newHTTPLokiTailer(wsBase, websocket.DefaultDialer)

	calls := 0
	err := tailer.Tail(context.Background(), `{}`, time.Now(), func(lokiTailMessage) error {
		calls++
		return assert.AnError
	})
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, calls, "onMessage's error must stop the tail after the first message, not be swallowed")
}
