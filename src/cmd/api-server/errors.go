// src/cmd/api-server/errors.go
package main

import (
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func writeJSON(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// writeGRPCError translates a gRPC error's status code into an HTTP
// response: codes.NotFound -> 404, codes.InvalidArgument -> 400,
// everything else -> 502 (the backend is reachable but returned something
// this layer has no more specific mapping for).
func writeGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	switch st.Code() {
	case codes.NotFound:
		writeJSONError(w, http.StatusNotFound, st.Message())
	case codes.InvalidArgument:
		writeJSONError(w, http.StatusBadRequest, st.Message())
	case codes.AlreadyExists:
		writeJSONError(w, http.StatusConflict, st.Message())
	default:
		writeJSONError(w, http.StatusBadGateway, st.Message())
	}
}
