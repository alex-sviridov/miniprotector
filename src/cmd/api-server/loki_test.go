package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPLokiClient_QueryRange_ParsesStreamsAndStructuredMetadata(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, `{binary=~"agent"} | event="finish"`, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "streams",
				"result": [
					{
						"stream": {"hostname": "webserver", "binary": "agent"},
						"values": [
							["1752400500000000000", "policy execution completed", {"job_id": "operating-refresh:1752400500", "event": "finish", "status": "success"}]
						]
					}
				]
			}
		}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	streams, err := client.QueryRange(context.Background(), `{binary=~"agent"} | event="finish"`, time.Unix(0, 0), time.Unix(1, 0), 100)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	assert.Equal(t, "webserver", streams[0].Stream["hostname"])
	require.Len(t, streams[0].Values, 1)
	assert.Equal(t, int64(1752400500000000000), streams[0].Values[0].Timestamp)
	assert.Equal(t, "policy execution completed", streams[0].Values[0].Line)
	assert.Equal(t, "operating-refresh:1752400500", streams[0].Values[0].Metadata["job_id"])
	assert.Equal(t, "finish", streams[0].Values[0].Metadata["event"])
	assert.Equal(t, "success", streams[0].Values[0].Metadata["status"])
}

func TestHTTPLokiClient_QueryRange_HandlesValuesWithNoStructuredMetadata(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"stream":{},"values":[["1","line with no metadata"]]}]}}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	streams, err := client.QueryRange(context.Background(), `{}`, time.Unix(0, 0), time.Unix(1, 0), 100)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Values, 1)
	assert.Nil(t, streams[0].Values[0].Metadata)
}

func TestHTTPLokiClient_QueryRange_NonOKStatusReturnsError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("loki unreachable"))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	_, err := client.QueryRange(context.Background(), `{}`, time.Unix(0, 0), time.Unix(1, 0), 100)
	assert.Error(t, err)
}

func TestHTTPLokiClient_QueryRange_SendsStartEndAsUnixNanoAndLimit(t *testing.T) {
	var gotQuery, gotStart, gotEnd, gotLimit string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotStart = r.URL.Query().Get("start")
		gotEnd = r.URL.Query().Get("end")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	_, err := client.QueryRange(context.Background(), `{binary="agent"}`, start, end, 42)
	require.NoError(t, err)

	assert.Equal(t, `{binary="agent"}`, gotQuery)
	assert.Equal(t, "1000000000000", gotStart)
	assert.Equal(t, "2000000000000", gotEnd)
	assert.Equal(t, "42", gotLimit)
}
