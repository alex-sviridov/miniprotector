package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type RequestHandlerFunc func(context.Context, pb.BackupService_ProcessBackupStreamServer, *pb.FileRequest) error

type streamHandler struct {
	config          *config.Config
	store           storage.BackupStore
	logger          *slog.Logger
	jobID           string
	currentFile     *filesystem.FileInfo
	currentExpireAt int64       // client-computed expiry for currentFile, from FileInfo.expire_at
	order           *chunkOrder // folds chunk CRCs into the file CRC32 in index order
	EOF             bool
	handlerMap      map[string]RequestHandlerFunc
}

func newStreamHandler(ctx context.Context, logger *slog.Logger, store storage.BackupStore, jobID string) *streamHandler {
	handler := &streamHandler{
		config: config.GetConfigFromContext(ctx),
		store:  store,
		logger: logger,
		jobID:  jobID,
	}
	handler.handlerMap = map[string]RequestHandlerFunc{
		fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}):  handler.handleFileInfoRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkHash{}): handler.handleChunkHashRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkData{}): handler.handleChunkDataRequest,
	}
	handler.logger.Info("New backup stream connected")
	return handler
}

// guardedStream releases the store operation guard (see
// storage.BackupStore.BeginBackupOp) at the first Send, or when the handler
// call returns, whichever comes first. Every handler finishes its store work
// before it replies, so the guard only needs to cover that part; holding it
// across a Send would let one client that has stopped reading keep a
// scheduled cleanup/vacuum batch waiting -- and a waiting exclusive lock
// blocks every other stream's next message too.
type guardedStream struct {
	pb.BackupService_ProcessBackupStreamServer
	end func()
}

func (g *guardedStream) release() {
	if g.end != nil {
		g.end()
		g.end = nil
	}
}

func (g *guardedStream) Send(m *pb.FileResponse) error {
	g.release()
	return g.BackupService_ProcessBackupStreamServer.Send(m)
}

// guarded takes the store operation guard for one handler call and wraps
// server so the guard is dropped before any reply goes out. The caller must
// defer the returned stream's release.
func (h *streamHandler) guarded(server pb.BackupService_ProcessBackupStreamServer) *guardedStream {
	return &guardedStream{BackupService_ProcessBackupStreamServer: server, end: h.store.BeginBackupOp()}
}

func (h *streamHandler) handleRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, request *pb.FileRequest) error {
	guarded := h.guarded(server)
	defer guarded.release()
	server = guarded
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
	h.currentExpireAt = fi.GetExpireAt()
	h.order = newChunkOrder()
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
		if err := h.store.EnsureFileVersion(
			h.jobID,
			h.currentFile.ID(),
			h.currentFile.Source(),
			h.currentFile.Path(),
			fmt.Sprintf("%c", h.currentFile.GetType()),
			h.currentFile.MetadataBlob(),
			h.currentFile.Ctime(),
			h.currentExpireAt,
		); err != nil {
			return fmt.Errorf("ensure file version: %w", err)
		}
		// Reset state before sending responses — fileWritten must not be called for skip-path
		// files because no FileDataRecord was created and fileWritten would create a duplicate FileVersion.
		fileID := fi.FileId
		h.order = nil
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
		// Chunk already stored — account for its checksum in the file hash.
		// brfs sent the checksum alongside the hash so we don't need the data.
		// With a window it may arrive ahead of an earlier chunk whose data is
		// still in flight; chunkOrder restores index order.
		if err := h.order.add(chunk.Index, chunk.Size, chunk.Checksum, chunk.Eof); err != nil {
			return err
		}
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
	h.EOF = h.order.done
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
	if err := h.order.add(chunk.Index, int64(len(chunk.Data)), crc32.ChecksumIEEE(chunk.Data), chunk.Eof); err != nil {
		return err
	}
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
	if h.order.done {
		chunkLogger.Debug("All chunks received")
		h.EOF = true
	}
	return server.Send(response)
}

func (h *streamHandler) fileWritten(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer) error {
	guarded := h.guarded(server)
	defer guarded.release()
	server = guarded
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], h.order.sum32())
	fileHash := buf[:]

	if err := h.store.FinalizeFileData(h.currentFile.ID(), fileHash); err != nil {
		return fmt.Errorf("finalize file data: %w", err)
	}
	// Record this file in the backup catalog now that its content is safely stored.
	if err := h.store.EnsureFileVersion(
		h.jobID,
		h.currentFile.ID(),
		h.currentFile.Source(),
		h.currentFile.Path(),
		fmt.Sprintf("%c", h.currentFile.GetType()),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
		h.currentExpireAt,
	); err != nil {
		return fmt.Errorf("ensure file version: %w", err)
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
	h.order = nil
	h.currentFile = nil
	h.EOF = false
	return message
}
