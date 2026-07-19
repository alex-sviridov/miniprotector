// src/cmd/api-server/errors_test.go
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWriteGRPCError_NotFoundMapsTo404(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.NotFound, "client x not found"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWriteGRPCError_InvalidArgumentMapsTo400(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.InvalidArgument, "bad input"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWriteGRPCError_OtherCodesMapTo502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.Unavailable, "backend down"))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestWriteGRPCError_NonGRPCErrorMapsTo502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, errors.New("plain error"))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestWriteGRPCError_AlreadyExistsMapsTo409(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.AlreadyExists, "client node-1 already enrolled"))
	assert.Equal(t, http.StatusConflict, rec.Code)
}
