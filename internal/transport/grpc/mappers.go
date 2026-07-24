package grpc

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// parseUUID converts a required wire ID, mapping malformed input to
// InvalidArgument (not Internal).
func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: malformed id %q", domain.ErrInvalidArgument, s)
	}
	return id, nil
}

// Mapping between entities and wire messages is centralized here so a schema
// change breaks exactly one file.

func fileToProto(f *entity.File) *stratov1.FileMetadata {
	m := &stratov1.FileMetadata{
		Id:             f.ID.String(),
		Name:           f.Name,
		MimeType:       f.MimeType,
		SizeBytes:      f.SizeBytes,
		ChecksumSha256: hex.EncodeToString(f.ChecksumSHA256),
		VersionNumber:  int32(f.VersionNumber), //nolint:gosec // bounded
		IsDeleted:      f.IsDeleted,
		IsLocked:       f.LockedBy != nil,
		CreatedAt:      timestamppb.New(f.CreatedAt),
		UpdatedAt:      timestamppb.New(f.UpdatedAt),
	}
	if f.FolderID != nil {
		m.FolderId = f.FolderID.String()
	}
	if f.LockedBy != nil {
		m.LockedByUserId = f.LockedBy.String()
	}
	if f.DeletedAt != nil {
		m.DeletedAt = timestamppb.New(*f.DeletedAt)
	}
	return m
}

func filesToProto(files []*entity.File) []*stratov1.FileMetadata {
	out := make([]*stratov1.FileMetadata, len(files))
	for i, f := range files {
		out[i] = fileToProto(f)
	}
	return out
}

func folderToProto(f *entity.Folder) *stratov1.FolderMetadata {
	m := &stratov1.FolderMetadata{
		Id:        f.ID.String(),
		Name:      f.Name,
		CreatedAt: timestamppb.New(f.CreatedAt),
	}
	if f.ParentID != nil {
		m.ParentId = f.ParentID.String()
	}
	return m
}

func versionToProto(v *entity.Version) *stratov1.FileVersion {
	return &stratov1.FileVersion{
		Id:              v.ID.String(),
		FileId:          v.FileID.String(),
		VersionNumber:   int32(v.VersionNumber), //nolint:gosec // bounded
		SizeBytes:       v.SizeBytes,
		ChecksumSha256:  hex.EncodeToString(v.ChecksumSHA256),
		CreatedByUserId: v.CreatedBy.String(),
		CreatedAt:       timestamppb.New(v.CreatedAt),
	}
}

func shareToProto(s *entity.Share, granteeEmail, publicURL string) *stratov1.Share {
	m := &stratov1.Share{
		Id:           s.ID.String(),
		FileId:       s.FileID.String(),
		OwnerId:      s.OwnerID.String(),
		GranteeEmail: granteeEmail,
		PublicUrl:    publicURL,
		Permission:   permissionToProto(s.Permission),
		CreatedAt:    timestamppb.New(s.CreatedAt),
	}
	if s.ExpiresAt != nil {
		m.ExpiresAt = timestamppb.New(*s.ExpiresAt)
	}
	return m
}

func permissionToProto(p entity.Permission) stratov1.Permission {
	switch p {
	case entity.PermissionViewer:
		return stratov1.Permission_PERMISSION_VIEWER
	case entity.PermissionEditor:
		return stratov1.Permission_PERMISSION_EDITOR
	case entity.PermissionOwner:
		return stratov1.Permission_PERMISSION_OWNER
	default:
		return stratov1.Permission_PERMISSION_UNSPECIFIED
	}
}

func permissionFromProto(p stratov1.Permission) entity.Permission {
	switch p {
	case stratov1.Permission_PERMISSION_VIEWER:
		return entity.PermissionViewer
	case stratov1.Permission_PERMISSION_EDITOR:
		return entity.PermissionEditor
	case stratov1.Permission_PERMISSION_OWNER:
		return entity.PermissionOwner
	default:
		return ""
	}
}

// pageFromProto decodes the wire pagination request.
func pageFromProto(p *stratov1.PageRequest) (pagination.Cursor, int, error) {
	if p == nil {
		return pagination.Cursor{}, pagination.DefaultPageSize, nil
	}
	cur, err := pagination.Decode(p.GetPageToken())
	if err != nil {
		return pagination.Cursor{}, 0, err
	}
	return cur, pagination.ClampPageSize(p.GetPageSize()), nil
}

// nextPageToken derives the next cursor from the last item of a full page.
// A short page means the listing is exhausted.
func nextPageToken(count, limit int, lastCreated time.Time, lastID string) *stratov1.PageResponse {
	if count < limit {
		return &stratov1.PageResponse{}
	}
	id, err := parseUUID(lastID)
	if err != nil {
		return &stratov1.PageResponse{}
	}
	return &stratov1.PageResponse{
		NextPageToken: pagination.Cursor{CreatedAt: lastCreated, ID: id}.Encode(),
	}
}

func optionalTime(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}
