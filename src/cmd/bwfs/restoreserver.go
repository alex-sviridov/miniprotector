package main

import (
	"encoding/hex"
	"errors"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type restoreServer struct {
	pb.UnimplementedRestoreServiceServer
	store  *wfs.Store
	logger *slog.Logger
}

func NewRestoreServer(store *wfs.Store, logger *slog.Logger) *restoreServer {
	return &restoreServer{store: store, logger: logger}
}

type fileDataRow struct {
	UUID       string `gorm:"column:uuid"`
	FileID     string `gorm:"column:file_id"`
	Size       int64  `gorm:"column:size"`
	ChunkCount int    `gorm:"column:chunk_count"`
	Checksum   []byte `gorm:"column:checksum"`
}

func (s *restoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	logger := s.logger.With("file_uuid", req.GetFileUuid())

	var fd fileDataRow
	err := s.store.RawDB().Table("file_data_records").
		Select("uuid, file_id, size, chunk_count, checksum").
		Where("uuid = ? AND checksum IS NOT NULL", req.GetFileUuid()).
		First(&fd).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Errorf(codes.NotFound, "file_uuid not found or unfinalized: %s", req.GetFileUuid())
		}
		return status.Errorf(codes.Internal, "db error looking up file_uuid: %v", err)
	}

	if err := stream.Send(&pb.RestoreEvent{
		Payload: &pb.RestoreEvent_Meta{
			Meta: &pb.RestoreFileMeta{
				Size:             fd.Size,
				ChunkCount:       int32(fd.ChunkCount),
				ExpectedChecksum: fd.Checksum,
			},
		},
	}); err != nil {
		return err
	}

	// One query locates every chunk of the file; a lookup per chunk was a
	// large share of the restore's time. A link whose chunk row is missing
	// is still listed, so it fails (and is marked) at its position below.
	chunks, err := s.store.LocateFileChunks(fd.FileID)
	if err != nil {
		return status.Errorf(codes.Internal, "query chunks: %v", err)
	}

	for i, chunk := range chunks {
		data, err := readRestoreChunk(s.store, logger, chunk)
		if err != nil {
			return status.Errorf(codes.Internal, "read chunk %x: %v", chunk.Hash, err)
		}

		eof := i == len(chunks)-1
		if err := stream.Send(&pb.RestoreEvent{
			Payload: &pb.RestoreEvent_Chunk{
				Chunk: &pb.RestoreChunk{
					Index: chunk.Index,
					Hash:  chunk.Hash,
					Data:  data,
					Eof:   eof,
				},
			},
		}); err != nil {
			return err
		}
	}

	logger.Debug("restore stream complete", "chunks", len(chunks))
	return nil
}

// restoreChunkSource is the part of the store readRestoreChunk needs.
type restoreChunkSource interface {
	ReadLocatedChunk(chunk wfs.FileChunk) ([]byte, error)
	MarkChunkCorrupted(hash []byte) error
}

// readRestoreChunk reads one chunk located by LocateFileChunks. The store
// still verifies its hash, although the client checks it too: the client can
// only fail the restore, while the store can mark the bad chunk so the next
// backup uploads it again.
//
// Only a chunk that is lost for good (storage.ErrChunkCorrupt) or no longer
// indexed (storage.ErrChunkNotFound) is marked corrupted, which drops it and
// invalidates the files using it so the next backup uploads them again.
// Not-found must keep marking: it is how a restore that races a backup
// linking a just-dropped chunk heals. Any other error (I/O, too many open
// files, database busy) may be transient, so it only fails this request.
func readRestoreChunk(src restoreChunkSource, logger *slog.Logger, chunk wfs.FileChunk) ([]byte, error) {
	hash := chunk.Hash
	data, err := src.ReadLocatedChunk(chunk)
	if err == nil {
		return data, nil
	}
	logger.Error("read chunk failed", "chunk_hash", hex.EncodeToString(hash), "error", err)
	if !errors.Is(err, storage.ErrChunkCorrupt) && !errors.Is(err, storage.ErrChunkNotFound) {
		return nil, err
	}
	if markErr := src.MarkChunkCorrupted(hash); markErr != nil {
		logger.Error("mark chunk corrupted failed", "chunk_hash", hex.EncodeToString(hash), "error", markErr)
	}
	return nil, err
}
