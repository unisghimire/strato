package grpc

import (
	"context"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/usecase"
	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// FileHandler exposes FileService over gRPC.
type FileHandler struct {
	stratov1.UnimplementedFileServiceServer
	files   *usecase.FileUseCase
	uploads *usecase.UploadUseCase
}

// NewFileHandler constructs a FileHandler.
func NewFileHandler(files *usecase.FileUseCase, uploads *usecase.UploadUseCase) *FileHandler {
	return &FileHandler{files: files, uploads: uploads}
}

// --- Upload lifecycle ---

// InitUpload opens a resumable upload session.
func (h *FileHandler) InitUpload(ctx context.Context, req *stratov1.InitUploadRequest) (*stratov1.InitUploadResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	res, err := h.uploads.InitUpload(ctx, ident, req.GetName(), req.GetFolderId(),
		req.GetMimeType(), req.GetSizeBytes(), req.GetChecksumSha256())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.InitUploadResponse{
		SessionId:     res.Session.ID.String(),
		ChunkSize:     res.Session.ChunkSize,
		TotalChunks:   int32(res.Session.TotalChunks), //nolint:gosec // bounded
		AlreadyExists: res.AlreadyExists,
	}, nil
}

// GetUploadStatus reports received chunks for client-side resume.
func (h *FileHandler) GetUploadStatus(ctx context.Context, req *stratov1.GetUploadStatusRequest) (*stratov1.GetUploadStatusResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	sessionID, err := parseUUID(req.GetSessionId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	sess, received, err := h.uploads.Status(ctx, ident, sessionID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	indexes := make([]int32, len(received))
	for i, n := range received {
		indexes[i] = int32(n) //nolint:gosec // bounded by total_chunks
	}
	return &stratov1.GetUploadStatusResponse{
		SessionId:      sess.ID.String(),
		ReceivedChunks: indexes,
		TotalChunks:    int32(sess.TotalChunks), //nolint:gosec // bounded
		Status:         string(sess.Status),
	}, nil
}

// CompleteUpload finalizes a session into a file version.
func (h *FileHandler) CompleteUpload(ctx context.Context, req *stratov1.CompleteUploadRequest) (*stratov1.CompleteUploadResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	sessionID, err := parseUUID(req.GetSessionId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	file, err := h.uploads.Complete(ctx, ident, sessionID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.CompleteUploadResponse{File: fileToProto(file)}, nil
}

// AbortUpload cancels a session.
func (h *FileHandler) AbortUpload(ctx context.Context, req *stratov1.AbortUploadRequest) (*stratov1.AbortUploadResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	sessionID, err := parseUUID(req.GetSessionId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.uploads.Abort(ctx, ident, sessionID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.AbortUploadResponse{}, nil
}

// --- File metadata ---

// GetFile returns metadata for one file.
func (h *FileHandler) GetFile(ctx context.Context, req *stratov1.GetFileRequest) (*stratov1.GetFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	f, err := h.files.Get(ctx, ident, fileID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.GetFileResponse{File: fileToProto(f)}, nil
}

// ListFiles pages files in a folder.
func (h *FileHandler) ListFiles(ctx context.Context, req *stratov1.ListFilesRequest) (*stratov1.ListFilesResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	cur, limit, err := pageFromProto(req.GetPage())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	fid, err := optionalUUID(req.GetFolderId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	files, err := h.files.List(ctx, ident, fid, req.GetIncludeDeleted(),
		req.GetOrder() == stratov1.SortOrder_SORT_ORDER_DESC, cur, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &stratov1.ListFilesResponse{Files: filesToProto(files), Page: &stratov1.PageResponse{}}
	if len(files) == limit {
		last := files[len(files)-1]
		resp.Page = nextPageToken(len(files), limit, last.CreatedAt, last.ID.String())
	}
	return resp, nil
}

// SearchFiles matches file names by substring.
func (h *FileHandler) SearchFiles(ctx context.Context, req *stratov1.SearchFilesRequest) (*stratov1.SearchFilesResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	cur, limit, err := pageFromProto(req.GetPage())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	files, err := h.files.Search(ctx, ident, req.GetQuery(), req.GetMimeType(), cur, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &stratov1.SearchFilesResponse{Files: filesToProto(files), Page: &stratov1.PageResponse{}}
	if len(files) == limit {
		last := files[len(files)-1]
		resp.Page = nextPageToken(len(files), limit, last.CreatedAt, last.ID.String())
	}
	return resp, nil
}

// MoveFile re-parents a file.
func (h *FileHandler) MoveFile(ctx context.Context, req *stratov1.MoveFileRequest) (*stratov1.MoveFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	target, err := optionalUUID(req.GetTargetFolderId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	f, err := h.files.Move(ctx, ident, fileID, target)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.MoveFileResponse{File: fileToProto(f)}, nil
}

// RenameFile renames a file.
func (h *FileHandler) RenameFile(ctx context.Context, req *stratov1.RenameFileRequest) (*stratov1.RenameFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	f, err := h.files.Rename(ctx, ident, fileID, req.GetNewName())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RenameFileResponse{File: fileToProto(f)}, nil
}

// DeleteFile soft-deletes a file.
func (h *FileHandler) DeleteFile(ctx context.Context, req *stratov1.DeleteFileRequest) (*stratov1.DeleteFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.files.Delete(ctx, ident, fileID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.DeleteFileResponse{}, nil
}

// RestoreFile un-deletes a file from trash.
func (h *FileHandler) RestoreFile(ctx context.Context, req *stratov1.RestoreFileRequest) (*stratov1.RestoreFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	f, err := h.files.Restore(ctx, ident, fileID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RestoreFileResponse{File: fileToProto(f)}, nil
}

// GetDownloadURL mints a short-lived signed download URL.
func (h *FileHandler) GetDownloadURL(ctx context.Context, req *stratov1.GetDownloadURLRequest) (*stratov1.GetDownloadURLResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	query, ttl, err := h.files.SignedDownloadQuery(ctx, ident, fileID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.GetDownloadURLResponse{
		Url:              "/v1/files/" + fileID.String() + "/content?" + query.Encode(),
		ExpiresInSeconds: int64(ttl.Seconds()),
	}, nil
}

// --- Versioning ---

// ListVersions pages a file's immutable history.
func (h *FileHandler) ListVersions(ctx context.Context, req *stratov1.ListVersionsRequest) (*stratov1.ListVersionsResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	cur, limit, err := pageFromProto(req.GetPage())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	versions, err := h.files.ListVersions(ctx, ident, fileID, cur, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	out := make([]*stratov1.FileVersion, len(versions))
	for i, v := range versions {
		out[i] = versionToProto(v)
	}
	resp := &stratov1.ListVersionsResponse{Versions: out, Page: &stratov1.PageResponse{}}
	if len(versions) == limit {
		last := versions[len(versions)-1]
		resp.Page = nextPageToken(len(versions), limit, last.CreatedAt, last.ID.String())
	}
	return resp, nil
}

// RestoreVersion promotes an old version to current.
func (h *FileHandler) RestoreVersion(ctx context.Context, req *stratov1.RestoreVersionRequest) (*stratov1.RestoreVersionResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	versionID, err := parseUUID(req.GetVersionId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	f, err := h.files.RestoreVersion(ctx, ident, fileID, versionID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RestoreVersionResponse{File: fileToProto(f)}, nil
}

// --- Locking ---

// LockFile takes the advisory write lock.
func (h *FileHandler) LockFile(ctx context.Context, req *stratov1.LockFileRequest) (*stratov1.LockFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.files.Lock(ctx, ident, fileID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.LockFileResponse{}, nil
}

// UnlockFile releases the advisory write lock.
func (h *FileHandler) UnlockFile(ctx context.Context, req *stratov1.UnlockFileRequest) (*stratov1.UnlockFileResponse, error) {
	ident, fileID, err := h.identAndID(ctx, req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.files.Unlock(ctx, ident, fileID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.UnlockFileResponse{}, nil
}

// --- Folders ---

// CreateFolder creates a folder.
func (h *FileHandler) CreateFolder(ctx context.Context, req *stratov1.CreateFolderRequest) (*stratov1.CreateFolderResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	parent, err := optionalUUID(req.GetParentId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	folder, err := h.files.CreateFolder(ctx, ident, req.GetName(), parent)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.CreateFolderResponse{Folder: folderToProto(folder)}, nil
}

// ListFolder returns subfolders plus a page of files.
func (h *FileHandler) ListFolder(ctx context.Context, req *stratov1.ListFolderRequest) (*stratov1.ListFolderResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	folderID, err := optionalUUID(req.GetFolderId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	cur, limit, err := pageFromProto(req.GetPage())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	folders, files, err := h.files.ListFolder(ctx, ident, folderID, cur, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	outFolders := make([]*stratov1.FolderMetadata, len(folders))
	for i, f := range folders {
		outFolders[i] = folderToProto(f)
	}
	resp := &stratov1.ListFolderResponse{
		Folders: outFolders,
		Files:   filesToProto(files),
		Page:    &stratov1.PageResponse{},
	}
	if len(files) == limit {
		last := files[len(files)-1]
		resp.Page = nextPageToken(len(files), limit, last.CreatedAt, last.ID.String())
	}
	return resp, nil
}

// DeleteFolder soft-deletes an empty folder.
func (h *FileHandler) DeleteFolder(ctx context.Context, req *stratov1.DeleteFolderRequest) (*stratov1.DeleteFolderResponse, error) {
	ident, folderID, err := h.identAndID(ctx, req.GetFolderId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.files.DeleteFolder(ctx, ident, folderID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.DeleteFolderResponse{}, nil
}

// --- helpers ---

func (h *FileHandler) identAndID(ctx context.Context, rawID string) (*domain.Identity, uuid.UUID, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := parseUUID(rawID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	return ident, id, nil
}

func optionalUUID(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // absent is a valid, distinct state
	}
	id, err := parseUUID(s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
