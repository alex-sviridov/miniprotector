package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/checksum"
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
	fileChecksumHasher hash.Hash32 // incremental CRC32 over chunk checksums
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
	h.fileChecksumHasher = crc32.NewIEEE()
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	fileLogger.Debug("Received file metadata", "file_info", fmt.Sprintf("%s", h.currentFile))

	fileExists, err := h.store.FileDataExists(h.currentFile.ID())
	if err != nil {
		return err
	}

	needed := !fileExists
	// Do not request transmission of non-file objects (dirs, symlinks, etc.)
	if h.currentFile.GetType() != 'f' {
		needed = false
	}
	// Empty files have no chunks to transfer
	if h.currentFile.Size() == 0 {
		needed = false
	}
	fileLogger.Debug("File existence check",
		"exists", fileExists,
		"needed", needed,
		"file_size", h.currentFile.Size(),
		"file_type", fmt.Sprintf("%c", h.currentFile.GetType()))

	if needed {
		// Create the incomplete FileData row; FinalizeFileData will complete it after all chunks arrive.
		if err := h.store.CreateFileData(h.currentFile.ID(), h.currentFile.Size()); err != nil {
			return fmt.Errorf("create file data: %w", err)
		}
	} else {
		// File already known or non-transferable — record it in the backup catalog now.
		if _, err := h.store.CreateFileVersion(
			h.currentFile.ID(),
			h.currentFile.ID(),
			h.currentFile.MetadataBlob(),
			h.currentFile.Ctime(),
		); err != nil {
			return fmt.Errorf("create file version: %w", err)
		}
		// Reset state before sending responses — fileWritten must not be called for skip-path
		// files because no FileDataRecord was created and fileWritten would create a duplicate FileVersion.
		fileID := fi.FileId
		h.fileChecksumHasher = nil
		h.currentFile = nil
		h.EOF = false
		// brfs always calls getFileStatus after sendFileMetadata, so we must send both
		// FileNeeded and FileProcessingResult before brfs can proceed to the next file.
		if err := server.Send(&pb.FileResponse{
			ResponseType: &pb.FileResponse_FileNeeded{
				FileNeeded: &pb.FileNeeded{FileId: fileID, Needed: false},
			},
		}); err != nil {
			return err
		}
		return server.Send(&pb.FileResponse{
			ResponseType: &pb.FileResponse_Result{
				Result: &pb.FileProcessingResult{FileId: fileID, Success: true},
			},
		})
	}

	return server.Send(&pb.FileResponse{
		ResponseType: &pb.FileResponse_FileNeeded{
			FileNeeded: &pb.FileNeeded{
				FileId: fi.FileId,
				Needed: needed,
			},
		},
	})
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
		// Chunk already stored — feed its checksum into the running file hash.
		// brfs sent the checksum alongside the hash so we don't need the data.
		checksum.FeedChunk(h.fileChecksumHasher, chunk.Checksum)
		// Must still link this chunk to the current file even though the data is
		// already stored; without this the restore server can't find it later.
		if err := h.store.LinkChunkToFileData(chunk.Hash, h.currentFile.ID(), chunk.Index); err != nil {
			return err
		}
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
	// Compute CRC32 from the received data — the authoritative source for new chunks.
	checksum.FeedChunk(h.fileChecksumHasher, crc32.ChecksumIEEE(chunk.Data))
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

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], h.fileChecksumHasher.Sum32())
	fileHash := buf[:]

	if err := h.store.FinalizeFileData(h.currentFile.ID(), fileHash); err != nil {
		return fmt.Errorf("finalize file data: %w", err)
	}
	// Record this file in the backup catalog now that its content is safely stored.
	if _, err := h.store.CreateFileVersion(
		h.currentFile.ID(),
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
		return fmt.Errorf("create file version: %w", err)
	}
	fileLogger.Debug("File transfer completed", "fileHash", hex.EncodeToString(fileHash))
	message := server.Send(&pb.FileResponse{
		ResponseType: &pb.FileResponse_Result{
			Result: &pb.FileProcessingResult{
				FileId:  h.currentFile.ID(),
				Success: true,
				Hash:    fileHash,
			},
		},
	})
	h.fileChecksumHasher = nil
	h.currentFile = nil
	h.EOF = false
	return message
}
