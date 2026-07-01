package main

import (
	"encoding/hex"
	"errors"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
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

type chunkLinkRow struct {
	ChunkHash string `gorm:"column:chunk_hash"`
	Index     int64  `gorm:"column:index"`
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

	var links []chunkLinkRow
	if err := s.store.RawDB().Table("file_data_chunk_records").
		Select("chunk_hash, `index`").
		Where("file_id = ?", fd.FileID).
		Order("`index` ASC").
		Find(&links).Error; err != nil {
		return status.Errorf(codes.Internal, "query chunks: %v", err)
	}

	for i, link := range links {
		hash, err := hex.DecodeString(link.ChunkHash)
		if err != nil {
			return status.Errorf(codes.Internal, "decode chunk hash: %v", err)
		}

		data, err := s.store.ReadChunk(hash)
		if err != nil {
			logger.Error("read chunk failed", "chunk_hash", link.ChunkHash, "error", err)
			if markErr := s.store.MarkChunkCorrupted(hash); markErr != nil {
				logger.Error("mark chunk corrupted failed", "chunk_hash", link.ChunkHash, "error", markErr)
			}
			return status.Errorf(codes.Internal, "read chunk %s: %v", link.ChunkHash, err)
		}

		eof := i == len(links)-1
		if err := stream.Send(&pb.RestoreEvent{
			Payload: &pb.RestoreEvent_Chunk{
				Chunk: &pb.RestoreChunk{
					Index: link.Index,
					Hash:  hash,
					Data:  data,
					Eof:   eof,
				},
			},
		}); err != nil {
			return err
		}
	}

	logger.Debug("restore stream complete", "chunks", len(links))
	return nil
}
