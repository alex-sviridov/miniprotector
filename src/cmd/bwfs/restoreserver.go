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
	Damaged    bool   `gorm:"column:damaged"`
}

func (s *restoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	logger := s.logger.With("file_uuid", req.GetFileUuid())

	var fd fileDataRow
	err := s.store.RawDB().Table("file_data_records").
		Select("uuid, file_id, size, chunk_count, checksum, damaged_at IS NOT NULL AS damaged").
		Where("uuid = ? AND checksum IS NOT NULL", req.GetFileUuid()).
		First(&fd).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Errorf(codes.NotFound, "file_uuid not found or unfinalized: %s", req.GetFileUuid())
		}
		return status.Errorf(codes.Internal, "db error looking up file_uuid: %v", err)
	}
	// A flagged version lost a chunk for good: its links to that chunk are
	// gone, so streaming it would send a silently truncated file. Fail before
	// the meta event so the client never starts writing it. DataLoss (not
	// Internal) tells the client a retry cannot help.
	if fd.Damaged {
		return status.Errorf(codes.DataLoss, "backup data damaged: file_uuid %s lost a chunk; back the file up again", req.GetFileUuid())
	}

	// One query locates every chunk of the file; a lookup per chunk was a
	// large share of the restore's time. A link whose chunk row is missing
	// is still listed, so it fails (and is marked) at its position below.
	chunks, err := s.store.LocateFileChunks(fd.FileID)
	if err != nil {
		return status.Errorf(codes.Internal, "query chunks: %v", err)
	}
	// chunk_count is the link count at finalize. Fewer links now means a
	// chunk was marked after the lookup above (by a concurrent restore,
	// verify or compaction), so streaming the rest would send a truncated
	// file and report success. Locating before the meta event lets this fail
	// before the client writes anything, at no extra query. Only missing
	// links mean loss, so the check is "<": extra link rows never fail it.
	if len(chunks) < fd.ChunkCount {
		return status.Errorf(codes.DataLoss, "backup data damaged: chunk lost (file_uuid %s has %d of %d chunks)",
			req.GetFileUuid(), len(chunks), fd.ChunkCount)
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

	for i, chunk := range chunks {
		data, err := readRestoreChunk(s.store, logger, chunk)
		if chunkLost(err) {
			// readRestoreChunk marked the chunk, which flagged this file.
			return status.Errorf(codes.DataLoss, "backup data damaged: chunk %x: %v", chunk.Hash, err)
		}
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
// flags the files using it damaged so the next backup uploads them again.
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
	if !chunkLost(err) {
		return nil, err
	}
	if markErr := src.MarkChunkCorrupted(hash); markErr != nil {
		logger.Error("mark chunk corrupted failed", "chunk_hash", hex.EncodeToString(hash), "error", markErr)
	}
	return nil, err
}

// chunkLost reports whether a chunk read failed because the chunk is gone for
// good (corrupt, or no longer indexed), as opposed to a possibly transient
// error. Only a lost chunk is marked, and only it is reported as DataLoss.
func chunkLost(err error) bool {
	return errors.Is(err, storage.ErrChunkCorrupt) || errors.Is(err, storage.ErrChunkNotFound)
}
