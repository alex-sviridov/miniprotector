package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zeebo/blake3"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type RequestHandlerFunc func(context.Context, pb.BackupService_ProcessBackupStreamServer, *pb.FileRequest) error

type streamHandler struct {
	config            *config.Config
	store             storage.BackupStore
	logger            *slog.Logger
	currentFile       *filesystem.FileInfo
	incrementalHasher *blake3.Hasher
	EOF               bool
	handlerMap        map[string]RequestHandlerFunc
}

func newStreamHandler(ctx context.Context, logger *slog.Logger, store storage.BackupStore) *streamHandler {
	handler := &streamHandler{
		config: config.GetConfigFromContext(ctx),
		store:  store,
		logger: logger,
	}
	handler.handlerMap = map[string]RequestHandlerFunc{
		fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}):  handler.handleFileInfoRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkHash{}): handler.handleChunkHashRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkData{}): handler.handleChunkDataRequest,
	}
	handler.logger.Info("New backup stream connected")
	return handler
}

func (h *streamHandler) handleRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, request *pb.FileRequest) error {
	requestType := fmt.Sprintf("%T", request.RequestType)
	handler, ok := h.handlerMap[requestType]
	if !ok {
		return fmt.Errorf("unknown request type: %s", requestType)
	}
	return handler(ctx, server, request)
}

func (h *streamHandler) handleFileInfoRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	fi := req.GetFileInfo()
	if fi == nil {
		return fmt.Errorf("FileRequest_FileInfo has empty FileInfo")
	}

	fileInfo, err := filesystem.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return err
	}
	h.currentFile = fileInfo
	h.incrementalHasher = blake3.New()
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	fileLogger.Debug("Received file metadata", "file_info", fmt.Sprintf("%s", h.currentFile))

	fileExists, err := h.store.FileDataExists(h.currentFile.ID())
	if err != nil {
		return err
	}

	needed := !fileExists
	if h.currentFile.GetType() != 'f' {
		needed = false
	}
	if h.currentFile.Size() == 0 {
		needed = false
	}
	fileLogger.Debug("File existence check",
		"exists", fileExists,
		"needed", needed,
		"file_size", h.currentFile.Size(),
		"file_type", fmt.Sprintf("%c", h.currentFile.GetType()))

	if !needed {
		h.EOF = true
	}

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_FileNeeded{
			FileNeeded: &pb.FileNeeded{
				FileId: fi.FileId,
				Needed: needed,
			},
		},
	}
	return server.Send(response)
}

func (h *streamHandler) handleChunkHashRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	chunk := req.GetChunkHash()
	if chunk == nil {
		return fmt.Errorf("FileRequest_ChunkHash has empty ChunkHash")
	}
	chunkLogger := h.logger.
		With(slog.String("file_id", h.currentFile.ID())).
		With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash)))

	chunkLogger.Debug("Received chunk hash")
	var needed bool

	err := h.store.ChunkExists(chunk.Hash)
	if err != nil {
		if errors.Is(err, storage.ErrChunkNotFound) {
			needed = true
		} else {
			return err
		}
	} else {
		needed = false
		h.incrementalHasher.Write(chunk.Hash)
	}

	chunkLogger.Debug("Chunk existence check", "needed", needed)

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_ChunkNeeded{
			ChunkNeeded: &pb.ChunkNeeded{
				Hash:   chunk.Hash,
				Needed: needed,
			},
		},
	}
	if chunk.Eof && !needed {
		h.EOF = true
	}
	return server.Send(response)
}

func (h *streamHandler) handleChunkDataRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	chunk := req.GetChunkData()
	if chunk == nil {
		return fmt.Errorf("FileRequest_ChunkData has empty ChunkData")
	}

	chunkLogger := h.logger.
		With(slog.String("file_id", h.currentFile.ID())).
		With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash)))

	if err := h.store.StoreChunk(chunk.Hash, chunk.Data); err != nil {
		return err
	}
	h.incrementalHasher.Write(chunk.Hash)
	chunkLogger.Debug("Chunk written")

	if err := h.store.LinkChunkToFileData(chunk.Hash, h.currentFile.ID(), chunk.Index); err != nil {
		return err
	}
	chunkLogger.Debug("Chunk linked")

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_ChunkResult{
			ChunkResult: &pb.ChunkResult{
				Hash:    chunk.Hash,
				Success: true,
			},
		},
	}
	if chunk.Eof {
		chunkLogger.Debug("EOF received")
		h.EOF = true
	}
	return server.Send(response)
}

func (h *streamHandler) fileWritten(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer) error {
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	file_hash := h.incrementalHasher.Sum(nil)
	h.store.FinalizeFileData(h.currentFile.ID(), file_hash)
	fileLogger.Debug("File transfer completed", "file_hash", hex.EncodeToString(file_hash))
	message := server.Send(&pb.FileResponse{
		ResponseType: &pb.FileResponse_Result{
			Result: &pb.FileProcessingResult{
				FileId:  h.currentFile.ID(),
				Success: true,
				Hash:    file_hash,
			},
		},
	})
	h.incrementalHasher = nil
	h.currentFile = nil
	h.EOF = false
	return message
}
