// Package localfs 提供 Workspace 内受限导出文件存储。
package localfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	exportDirectory      = ".knowledge/exports"
	stagingDirectory     = exportDirectory + "/.staging"
	maxExportFileBytes   = 32 * 1024 * 1024
	maxStagingCandidates = 100
	filePathUnsafeCode   = "EXPORT_FILE_PATH_UNSAFE"
	fileIOFailedCode     = "EXPORT_FILE_IO_FAILED"
)

// WorkspaceReader 读取 Workspace 与其已规范化根路径。
type WorkspaceReader interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// Store 使用 0600 临时文件、fsync 和 create-only 原子链接写入固定导出目录。
type Store struct{ workspaces WorkspaceReader }

var _ exportapp.FileStore = (*Store)(nil)

// NewStore 创建拒绝目录 symlink 和越界路径的本地导出存储。
func NewStore(workspaces WorkspaceReader) (*Store, error) {
	if nilDependency(workspaces) {
		return nil, unavailable(errors.New("export workspace reader is unavailable"))
	}
	return &Store{workspaces: workspaces}, nil
}

// Write 原子写入固定任务文件；相同内容的已有文件按幂等成功处理。
func (store *Store) Write(ctx context.Context, workspaceID foundation.ID, relativePath string, payload []byte) (string, string, int64, error) {
	if err := validateRequest(ctx, workspaceID, relativePath); err != nil {
		return "", "", 0, err
	}
	if len(payload) > maxExportFileBytes {
		return "", "", 0, invalid(errors.New("export file exceeds the size limit"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return "", "", 0, err
	}
	defer root.Close()
	if err := ensureExportDirectories(root, true); err != nil {
		return "", "", 0, err
	}
	digest := sha256.Sum256(payload)
	expectedHash := hex.EncodeToString(digest[:])
	if _, statErr := root.Lstat(relativePath); statErr == nil {
		if verifyErr := verifyExisting(root, relativePath, expectedHash, int64(len(payload))); verifyErr != nil {
			return "", "", 0, verifyErr
		}
		return relativePath, expectedHash, int64(len(payload)), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", 0, ioFailure(statErr)
	}

	temporaryPath, err := reserveTemporaryPath(root, relativePath)
	if err != nil {
		return "", "", 0, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporaryPath)
		}
	}()
	file, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return "", "", 0, ioFailure(err)
	}
	writeErr := writeAndSync(file, payload)
	closeErr := file.Close()
	if writeErr != nil {
		return "", "", 0, ioFailure(writeErr)
	}
	if closeErr != nil {
		return "", "", 0, ioFailure(closeErr)
	}
	if err := root.Link(temporaryPath, relativePath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", "", 0, ioFailure(err)
		}
		if verifyErr := verifyExisting(root, relativePath, expectedHash, int64(len(payload))); verifyErr != nil {
			return "", "", 0, verifyErr
		}
		return relativePath, expectedHash, int64(len(payload)), nil
	}
	if err := root.Remove(temporaryPath); err != nil {
		return "", "", 0, ioFailure(err)
	}
	removeTemporary = false
	if err := syncDirectory(root, exportDirectory); err != nil {
		return "", "", 0, err
	}
	if err := verifyExisting(root, relativePath, expectedHash, int64(len(payload))); err != nil {
		return "", "", 0, err
	}
	return relativePath, expectedHash, int64(len(payload)), nil
}

// Stage 以 create-only 方式写入受控 staging 命名空间，供持久化 prepared binding 使用。
func (store *Store) Stage(ctx context.Context, workspaceID, exportID foundation.ID, extension string, payload []byte) (exportapp.PreparedFile, error) {
	if err := validateStageRequest(ctx, workspaceID, exportID, extension); err != nil {
		return exportapp.PreparedFile{}, err
	}
	if len(payload) > maxExportFileBytes {
		return exportapp.PreparedFile{}, invalid(errors.New("export file exceeds the size limit"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return exportapp.PreparedFile{}, err
	}
	defer root.Close()
	if err := ensureStagingDirectory(root, true); err != nil {
		return exportapp.PreparedFile{}, err
	}

	digest := sha256.Sum256(payload)
	prepared := exportapp.PreparedFile{
		FinalPath: finalPathFor(exportID, extension),
		FileHash:  hex.EncodeToString(digest[:]),
		FileSize:  int64(len(payload)),
	}
	for attempt := 0; attempt < 8; attempt++ {
		stagingPath, err := reserveStagingPath(exportID, extension)
		if err != nil {
			return exportapp.PreparedFile{}, err
		}
		file, err := root.OpenFile(stagingPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return exportapp.PreparedFile{}, ioFailure(err)
		}
		writeErr := writeAndSync(file, payload)
		closeErr := file.Close()
		if writeErr != nil {
			_ = root.Remove(stagingPath)
			return exportapp.PreparedFile{}, ioFailure(writeErr)
		}
		if closeErr != nil {
			_ = root.Remove(stagingPath)
			return exportapp.PreparedFile{}, ioFailure(closeErr)
		}
		if err := syncDirectory(root, stagingDirectory); err != nil {
			return exportapp.PreparedFile{}, err
		}
		if err := verifyExisting(root, stagingPath, prepared.FileHash, prepared.FileSize); err != nil {
			return exportapp.PreparedFile{}, err
		}
		prepared.StagingPath = stagingPath
		return prepared, nil
	}
	return exportapp.PreparedFile{}, unavailable(errors.New("could not reserve export staging path"))
}

// Promote 原子地把已持久化绑定的 staging 文件移动到固定最终路径。
func (store *Store) Promote(ctx context.Context, workspaceID foundation.ID, prepared exportapp.PreparedFile) error {
	if err := validatePreparedRequest(ctx, workspaceID, prepared); err != nil {
		return err
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := ensureExportDirectories(root, false); err != nil {
		return err
	}
	if err := ensureStagingDirectoryForPromote(root); err != nil {
		return err
	}

	stagingExists, err := managedFileExists(root, prepared.StagingPath)
	if err != nil {
		return err
	}
	if !stagingExists {
		return verifyPromotedFinal(root, prepared)
	}
	if err := verifyExisting(root, prepared.StagingPath, prepared.FileHash, prepared.FileSize); err != nil {
		return err
	}
	if err := root.Link(prepared.StagingPath, prepared.FinalPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return ioFailure(err)
		}
		if err := verifyExisting(root, prepared.FinalPath, prepared.FileHash, prepared.FileSize); err != nil {
			return err
		}
	}
	if err := root.Remove(prepared.StagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ioFailure(err)
	}
	if err := syncDirectory(root, exportDirectory); err != nil {
		return err
	}
	if err := syncDirectory(root, stagingDirectory); err != nil {
		return err
	}
	return verifyExisting(root, prepared.FinalPath, prepared.FileHash, prepared.FileSize)
}

// ListStaging 返回早于 cutoff 的严格命名 staging 文件，供 orphan sweep 有界处理。
func (store *Store) ListStaging(ctx context.Context, workspaceID foundation.ID, olderThan time.Time, limit int) ([]exportapp.StagingFile, error) {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return nil, err
	}
	if olderThan.IsZero() || limit < 1 || limit > maxStagingCandidates {
		return nil, invalid(errors.New("export staging list request is invalid"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := ensureStagingDirectory(root, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []exportapp.StagingFile{}, nil
		}
		return nil, err
	}
	entries, err := fs.ReadDir(root.FS(), stagingDirectory)
	if err != nil {
		return nil, ioFailure(err)
	}
	staging := make([]exportapp.StagingFile, 0, min(limit, len(entries)))
	for _, entry := range entries {
		candidate := stagingDirectory + "/" + entry.Name()
		if !validStagingPath(candidate) {
			continue
		}
		info, err := root.Lstat(candidate)
		if err != nil {
			return nil, ioFailure(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, unsafe(errors.New("export staging candidate is not a regular private file"))
		}
		if !info.ModTime().Before(olderThan) {
			continue
		}
		staging = append(staging, exportapp.StagingFile{Path: candidate, ModifiedAt: info.ModTime()})
		if len(staging) == limit {
			break
		}
	}
	return staging, nil
}

// Read 只读取固定目录内的普通 0600 文件，并复核 size 与 SHA-256。
func (store *Store) Read(ctx context.Context, workspaceID foundation.ID, relativePath, expectedHash string, expectedSize int64) ([]byte, error) {
	if err := validateManagedRequest(ctx, workspaceID, relativePath); err != nil {
		return nil, err
	}
	if !validHash(expectedHash) || expectedSize < 0 || expectedSize > maxExportFileBytes {
		return nil, invalid(errors.New("export file binding is invalid"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := ensureManagedDirectories(root, relativePath, false); err != nil {
		return nil, err
	}
	return readVerified(root, relativePath, expectedHash, expectedSize)
}

// DeletePrepared 仅删除仍与 durable prepared binding 完全匹配的受控文件。
func (store *Store) DeletePrepared(ctx context.Context, workspaceID foundation.ID, relativePath, expectedHash string, expectedSize int64) error {
	if err := validateManagedRequest(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	if !validHash(expectedHash) || expectedSize < 0 || expectedSize > maxExportFileBytes {
		return invalid(errors.New("export prepared file binding is invalid"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := ensureManagedDirectories(root, relativePath, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_, err = root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ioFailure(err)
	}
	if err := verifyExisting(root, relativePath, expectedHash, expectedSize); err != nil {
		return err
	}
	if err := root.Remove(relativePath); err != nil {
		return ioFailure(err)
	}
	return syncDirectory(root, directoryForManagedPath(relativePath))
}

// DeleteOrphan 只删除已经由 repository 证明未绑定的严格 staging 文件。
func (store *Store) DeleteOrphan(ctx context.Context, workspaceID foundation.ID, relativePath string) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validStagingPath(relativePath) {
		return invalid(errors.New("export orphan path is invalid"))
	}
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := ensureStagingDirectory(root, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return unsafe(errors.New("export orphan is not a regular private staging file"))
	}
	if err := root.Remove(relativePath); err != nil {
		return ioFailure(err)
	}
	return syncDirectory(root, stagingDirectory)
}

func (store *Store) openWorkspaceRoot(ctx context.Context, workspaceID foundation.ID) (*os.Root, error) {
	if store == nil || nilDependency(store.workspaces) {
		return nil, unavailable(errors.New("export file store is unavailable"))
	}
	workspace, err := store.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	rootPath := strings.TrimSpace(workspace.RootPath)
	if workspace.ID != workspaceID || rootPath == "" || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return nil, unsafe(errors.New("workspace root binding is invalid"))
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, unsafe(errors.New("workspace root is not a regular directory"))
	}
	canonical, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return nil, ioFailure(err)
	}
	if canonical != rootPath {
		return nil, unsafe(errors.New("workspace root is not canonical"))
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, ioFailure(err)
	}
	return root, nil
}

func validateRequest(ctx context.Context, workspaceID foundation.ID, relativePath string) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validExportPath(relativePath) {
		return invalid(errors.New("export file request is invalid"))
	}
	return nil
}

func validateManagedRequest(ctx context.Context, workspaceID foundation.ID, relativePath string) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validManagedPath(relativePath) {
		return invalid(errors.New("export file request is invalid"))
	}
	return nil
}

func validateWorkspaceContext(ctx context.Context, workspaceID foundation.ID) error {
	if ctx == nil {
		return invalid(errors.New("export file context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(workspaceID) {
		return invalid(errors.New("export file request is invalid"))
	}
	return nil
}

func validateStageRequest(ctx context.Context, workspaceID, exportID foundation.ID, extension string) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validID(exportID) || !validExtension(extension) {
		return invalid(errors.New("export staging request is invalid"))
	}
	return nil
}

func validatePreparedRequest(ctx context.Context, workspaceID foundation.ID, prepared exportapp.PreparedFile) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validStagingPath(prepared.StagingPath) || !validExportPath(prepared.FinalPath) || !validHash(prepared.FileHash) || prepared.FileSize < 0 || prepared.FileSize > maxExportFileBytes {
		return invalid(errors.New("export prepared file binding is invalid"))
	}
	stagingID, stagingExtension, ok := stagingIdentity(prepared.StagingPath)
	finalID, finalExtension, okFinal := exportIdentity(prepared.FinalPath)
	if !ok || !okFinal || stagingID != finalID || stagingExtension != finalExtension {
		return invalid(errors.New("export prepared file paths are inconsistent"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validExportPath(value string) bool {
	_, _, ok := exportIdentity(value)
	return ok
}

func validStagingPath(value string) bool {
	_, _, ok := stagingIdentity(value)
	return ok
}

func validManagedPath(value string) bool {
	return validExportPath(value) || validStagingPath(value)
}

func exportIdentity(value string) (foundation.ID, string, bool) {
	if !validRelativePath(value) {
		return "", "", false
	}
	prefix := exportDirectory + "/"
	if !strings.HasPrefix(value, prefix) || strings.Contains(strings.TrimPrefix(value, prefix), "/") {
		return "", "", false
	}
	base := strings.TrimPrefix(value, prefix)
	extension := path.Ext(base)
	if !validExtension(extension) {
		return "", "", false
	}
	id, err := foundation.ParseID(strings.TrimSuffix(base, extension))
	if err != nil || string(id) != strings.TrimSuffix(base, extension) {
		return "", "", false
	}
	return id, extension, true
}

func stagingIdentity(value string) (foundation.ID, string, bool) {
	if !validRelativePath(value) {
		return "", "", false
	}
	prefix := stagingDirectory + "/"
	if !strings.HasPrefix(value, prefix) || strings.Contains(strings.TrimPrefix(value, prefix), "/") {
		return "", "", false
	}
	base := strings.TrimPrefix(value, prefix)
	if !strings.HasSuffix(base, ".stage") {
		return "", "", false
	}
	base = strings.TrimSuffix(base, ".stage")
	extension := path.Ext(base)
	if !validExtension(extension) {
		return "", "", false
	}
	identity := strings.TrimSuffix(base, extension)
	if len(identity) != 69 || identity[36] != '-' {
		return "", "", false
	}
	id, err := foundation.ParseID(identity[:36])
	if err != nil || string(id) != identity[:36] || !validRandomHex(identity[37:]) {
		return "", "", false
	}
	return id, extension, true
}

func validRelativePath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.Contains(value, "\\") && path.Clean(value) == value
}

func validExtension(extension string) bool {
	return extension == ".md" || extension == ".json"
}

func validRandomHex(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func finalPathFor(exportID foundation.ID, extension string) string {
	return exportDirectory + "/" + string(exportID) + extension
}

func directoryForManagedPath(relativePath string) string {
	if validStagingPath(relativePath) {
		return stagingDirectory
	}
	return exportDirectory
}

func ensureExportDirectories(root *os.Root, create bool) error {
	for _, directory := range []string{".knowledge", exportDirectory} {
		info, err := root.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) && create {
			if mkdirErr := root.Mkdir(directory, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return ioFailure(mkdirErr)
			}
			info, err = root.Lstat(directory)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return err
			}
			return ioFailure(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return unsafe(errors.New("export directory is a symlink or non-directory"))
		}
		if directory == exportDirectory && info.Mode().Perm()&0o077 != 0 {
			return unsafe(errors.New("export directory permissions are too broad"))
		}
	}
	return nil
}

func ensureStagingDirectory(root *os.Root, create bool) error {
	if err := ensureExportDirectories(root, create); err != nil {
		return err
	}
	info, err := root.Lstat(stagingDirectory)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := root.Mkdir(stagingDirectory, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return ioFailure(mkdirErr)
		}
		created = true
		info, err = root.Lstat(stagingDirectory)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return unsafe(errors.New("export staging directory is unsafe"))
	}
	if created {
		return syncDirectory(root, exportDirectory)
	}
	return nil
}

func ensureStagingDirectoryForPromote(root *os.Root) error {
	err := ensureStagingDirectory(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func ensureManagedDirectories(root *os.Root, relativePath string, create bool) error {
	if validStagingPath(relativePath) {
		return ensureStagingDirectory(root, create)
	}
	return ensureExportDirectories(root, create)
}

func reserveTemporaryPath(root *os.Root, target string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return "", unavailable(err)
		}
		candidate := fmt.Sprintf("%s.%s.tmp", target, hex.EncodeToString(random[:]))
		if _, err := root.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", ioFailure(err)
		}
	}
	return "", unavailable(errors.New("could not reserve export temporary path"))
}

func reserveStagingPath(exportID foundation.ID, extension string) (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", unavailable(err)
	}
	return fmt.Sprintf("%s/%s-%s%s.stage", stagingDirectory, exportID, hex.EncodeToString(random[:]), extension), nil
}

func writeAndSync(file *os.File, payload []byte) error {
	for len(payload) > 0 {
		written, err := file.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return file.Sync()
}

func verifyExisting(root *os.Root, relativePath, expectedHash string, expectedSize int64) error {
	_, err := readVerified(root, relativePath, expectedHash, expectedSize)
	return err
}

func managedFileExists(root *os.Root, relativePath string) (bool, error) {
	info, err := root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, unsafe(errors.New("export target is not a regular file"))
	}
	return true, nil
}

func verifyPromotedFinal(root *os.Root, prepared exportapp.PreparedFile) error {
	exists, err := managedFileExists(root, prepared.FinalPath)
	if err != nil {
		return err
	}
	if !exists {
		return inconsistent(errors.New("export staging and final files are both absent"))
	}
	return verifyExisting(root, prepared.FinalPath, prepared.FileHash, prepared.FileSize)
}

func readVerified(root *os.Root, relativePath, expectedHash string, expectedSize int64) ([]byte, error) {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return nil, ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, unsafe(errors.New("export file is not a regular file"))
	}
	if info.Mode().Perm() != 0o600 || info.Size() != expectedSize {
		return nil, inconsistent(errors.New("export file metadata does not match its binding"))
	}
	file, err := root.OpenFile(relativePath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ioFailure(err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, expectedSize+1))
	if err != nil {
		return nil, ioFailure(err)
	}
	if int64(len(payload)) != expectedSize {
		return nil, inconsistent(errors.New("export file size changed while reading"))
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return nil, inconsistent(errors.New("export file hash does not match its binding"))
	}
	return payload, nil
}

func syncDirectory(root *os.Root, directory string) error {
	info, err := root.Lstat(directory)
	if err != nil {
		return ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return unsafe(errors.New("export directory is a symlink or non-directory"))
	}
	opened, err := root.Open(directory)
	if err != nil {
		return ioFailure(err)
	}
	defer opened.Close()
	if err := opened.Sync(); err != nil {
		return ioFailure(err)
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func unsafe(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, filePathUnsafeCode, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func ioFailure(cause error) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, fileIOFailedCode, true, cause)
}

func inconsistent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, cause)
}
