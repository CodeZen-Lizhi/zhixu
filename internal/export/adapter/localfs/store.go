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
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"golang.org/x/sys/unix"
)

const (
	exportDirectory      = ".knowledge/exports"
	stagingDirectory     = exportDirectory + "/.staging"
	maxExportFileBytes   = 1024 * 1024 * 1024
	maxStagingCandidates = 100
	filePathUnsafeCode   = "EXPORT_FILE_PATH_UNSAFE"
	fileIOFailedCode     = "EXPORT_FILE_IO_FAILED"
)

var errStagingListComplete = errors.New("export staging list is complete")

// WorkspaceReader 读取 Workspace 与其已规范化根路径。
type WorkspaceReader interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// Store 使用 0600 临时文件、fsync 和 create-only 原子链接写入固定导出目录。
type Store struct {
	workspaces     WorkspaceReader
	attachmentHook attachmentArchiveHook
	secureHook     secureFilesystemHook
}

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
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, true, false)
	if err != nil {
		return "", "", 0, err
	}
	defer root.Close()
	defer managed.Close()
	directory, finalName := managedFileDirectory(managed, relativePath)
	digest := sha256.Sum256(payload)
	expectedHash := hex.EncodeToString(digest[:])
	if _, statErr := directory.Lstat(finalName); statErr == nil {
		if verifyErr := verifyExistingInDirectory(ctx, directory, finalName, expectedHash, int64(len(payload))); verifyErr != nil {
			return "", "", 0, verifyErr
		}
		if err := verifyManagedBindings(managed); err != nil {
			return "", "", 0, err
		}
		return relativePath, expectedHash, int64(len(payload)), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", 0, managedBoundaryError(statErr)
	}

	temporaryName, err := reserveTemporaryName(directory, finalName)
	if err != nil {
		return "", "", 0, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = directory.Remove(temporaryName)
		}
	}()
	file, err := directory.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", 0, managedBoundaryError(err)
	}
	writeErr := writeAndSync(file, payload)
	closeErr := file.Close()
	if writeErr != nil {
		return "", "", 0, ioFailure(writeErr)
	}
	if closeErr != nil {
		return "", "", 0, ioFailure(closeErr)
	}
	if err := directory.Link(temporaryName, directory, finalName); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", "", 0, managedBoundaryError(err)
		}
		if verifyErr := verifyExistingInDirectory(ctx, directory, finalName, expectedHash, int64(len(payload))); verifyErr != nil {
			return "", "", 0, verifyErr
		}
		if err := verifyManagedBindings(managed); err != nil {
			return "", "", 0, err
		}
		return relativePath, expectedHash, int64(len(payload)), nil
	}
	if err := directory.Remove(temporaryName); err != nil {
		return "", "", 0, managedBoundaryError(err)
	}
	removeTemporary = false
	if err := directory.Sync(); err != nil {
		return "", "", 0, managedBoundaryError(err)
	}
	if err := verifyExistingInDirectory(ctx, directory, finalName, expectedHash, int64(len(payload))); err != nil {
		return "", "", 0, err
	}
	if err := verifyManagedBindings(managed); err != nil {
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
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, true, true)
	if err != nil {
		return exportapp.PreparedFile{}, err
	}
	defer root.Close()
	defer managed.Close()

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
		stagingName := path.Base(stagingPath)
		file, err := managed.staging.OpenFile(stagingName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return exportapp.PreparedFile{}, managedBoundaryError(err)
		}
		writeErr := writeAndSync(file, payload)
		closeErr := file.Close()
		if writeErr != nil {
			_ = managed.staging.Remove(stagingName)
			return exportapp.PreparedFile{}, ioFailure(writeErr)
		}
		if closeErr != nil {
			_ = managed.staging.Remove(stagingName)
			return exportapp.PreparedFile{}, ioFailure(closeErr)
		}
		if err := managed.staging.Sync(); err != nil {
			return exportapp.PreparedFile{}, managedBoundaryError(err)
		}
		if err := verifyExistingInDirectory(ctx, managed.staging, stagingName, prepared.FileHash, prepared.FileSize); err != nil {
			return exportapp.PreparedFile{}, err
		}
		if err := verifyManagedBindings(managed); err != nil {
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
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil {
		return err
	}
	defer root.Close()
	defer managed.Close()
	stagingName := path.Base(prepared.StagingPath)
	finalName := path.Base(prepared.FinalPath)
	stagingExists, err := managedFileExistsInDirectory(managed.staging, stagingName)
	if err != nil {
		return err
	}
	if !stagingExists {
		if err := verifyExistingInDirectory(ctx, managed.exports, finalName, prepared.FileHash, prepared.FileSize); err != nil {
			return err
		}
		return verifyManagedBindings(managed)
	}
	if err := verifyExistingInDirectory(ctx, managed.staging, stagingName, prepared.FileHash, prepared.FileSize); err != nil {
		return err
	}
	if store.secureHook.afterManagedVerify != nil {
		store.secureHook.afterManagedVerify()
	}
	if err := managed.staging.Link(stagingName, managed.exports, finalName); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return managedBoundaryError(err)
		}
		if err := verifyExistingInDirectory(ctx, managed.exports, finalName, prepared.FileHash, prepared.FileSize); err != nil {
			return err
		}
	}
	if err := managed.staging.Remove(stagingName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return managedBoundaryError(err)
	}
	if err := managed.exports.Sync(); err != nil {
		return managedBoundaryError(err)
	}
	if err := managed.staging.Sync(); err != nil {
		return managedBoundaryError(err)
	}
	if err := verifyExistingInDirectory(ctx, managed.exports, finalName, prepared.FileHash, prepared.FileSize); err != nil {
		return err
	}
	return verifyManagedBindings(managed)
}

// ListStaging 返回早于 cutoff 的严格命名 staging 文件，供 orphan sweep 有界处理。
func (store *Store) ListStaging(ctx context.Context, workspaceID foundation.ID, olderThan time.Time, limit int) ([]exportapp.StagingFile, error) {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return nil, err
	}
	if olderThan.IsZero() || limit < 1 || limit > maxStagingCandidates {
		return nil, invalid(errors.New("export staging list request is invalid"))
	}
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, false, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []exportapp.StagingFile{}, nil
		}
		return nil, err
	}
	defer root.Close()
	defer managed.Close()
	staging := make([]exportapp.StagingFile, 0, limit)
	err = managed.staging.ForEachDirEntry(maxStagingCandidates, func(entry os.DirEntry) error {
		candidate := stagingDirectory + "/" + entry.Name()
		if !validStagingPath(candidate) {
			return nil
		}
		info, err := managed.staging.Lstat(entry.Name())
		if err != nil {
			return managedBoundaryError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return unsafe(errors.New("export staging candidate is not a regular private file"))
		}
		if !info.ModTime().Before(olderThan) {
			return nil
		}
		staging = append(staging, exportapp.StagingFile{Path: candidate, ModifiedAt: info.ModTime()})
		if len(staging) == limit {
			return errStagingListComplete
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStagingListComplete) {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return nil, err
		}
		return nil, ioFailure(err)
	}
	if err := verifyManagedBindings(managed); err != nil {
		return nil, err
	}
	return staging, nil
}

// Open 打开固定目录内的普通 0600 文件，完整复核 size 与 SHA-256 后返回同一文件描述符。
func (store *Store) Open(ctx context.Context, workspaceID foundation.ID, relativePath, expectedHash string, expectedSize int64) (io.ReadCloser, error) {
	if err := validateManagedRequest(ctx, workspaceID, relativePath); err != nil {
		return nil, err
	}
	if !validHash(expectedHash) || expectedSize < 0 || expectedSize > maxExportFileBytes {
		return nil, invalid(errors.New("export file binding is invalid"))
	}
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, false, validStagingPath(relativePath))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	defer managed.Close()
	directory, name := managedFileDirectory(managed, relativePath)
	file, err := openVerifiedInDirectory(ctx, directory, name, expectedHash, expectedSize)
	if err != nil {
		return nil, err
	}
	if err := verifyManagedBindings(managed); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// DeletePrepared 仅删除仍与 durable prepared binding 完全匹配的受控文件。
func (store *Store) DeletePrepared(ctx context.Context, workspaceID foundation.ID, relativePath, expectedHash string, expectedSize int64) error {
	if err := validateManagedRequest(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	if !validHash(expectedHash) || expectedSize < 0 || expectedSize > maxExportFileBytes {
		return invalid(errors.New("export prepared file binding is invalid"))
	}
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, false, validStagingPath(relativePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	defer managed.Close()
	directory, name := managedFileDirectory(managed, relativePath)
	_, err = directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return managedBoundaryError(err)
	}
	if err := verifyExistingInDirectory(ctx, directory, name, expectedHash, expectedSize); err != nil {
		return err
	}
	if store.secureHook.afterManagedVerify != nil {
		store.secureHook.afterManagedVerify()
	}
	if err := verifyManagedBindings(managed); err != nil {
		return err
	}
	// Move the currently named object aside atomically, then verify that exact
	// object again before unlinking it. A path swap can therefore never make
	// cleanup delete an unverified replacement.
	quarantine, err := quarantinePreparedFile(directory, name)
	if errors.Is(err, os.ErrNotExist) {
		return verifyManagedBindings(managed)
	}
	if err != nil {
		return err
	}
	restore := func(cause error) error {
		restoreErr := directory.RenameNoReplace(quarantine, name)
		if restoreErr == nil {
			restoreErr = directory.Sync()
		}
		if restoreErr != nil {
			return errors.Join(cause, managedBoundaryError(restoreErr))
		}
		return cause
	}
	if err := verifyExistingInDirectory(ctx, directory, quarantine, expectedHash, expectedSize); err != nil {
		return restore(err)
	}
	if err := verifyManagedBindings(managed); err != nil {
		return restore(err)
	}
	if store.secureHook.afterPreparedVerify != nil {
		store.secureHook.afterPreparedVerify()
	}
	if _, err := directory.Lstat(name); err == nil {
		cause := inconsistent(errors.New("export prepared path was replaced during cleanup"))
		if removeErr := directory.Remove(quarantine); removeErr != nil {
			return errors.Join(cause, managedBoundaryError(removeErr))
		}
		if syncErr := directory.Sync(); syncErr != nil {
			return errors.Join(cause, managedBoundaryError(syncErr))
		}
		return cause
	} else if !errors.Is(err, os.ErrNotExist) {
		return restore(managedBoundaryError(err))
	}
	if err := directory.Remove(quarantine); err != nil {
		return restore(managedBoundaryError(err))
	}
	if err := directory.Sync(); err != nil {
		return managedBoundaryError(err)
	}
	return verifyManagedBindings(managed)
}

func quarantinePreparedFile(directory *secureDir, name string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return "", unavailable(err)
		}
		candidate := fmt.Sprintf(".%s.%s.delete", name, hex.EncodeToString(random[:]))
		err := directory.RenameNoReplace(name, candidate)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", managedBoundaryError(err)
		}
	}
	return "", unavailable(errors.New("could not isolate export prepared file"))
}

// DeleteOrphan 只删除已经由 repository 证明未绑定的严格 staging 文件。
func (store *Store) DeleteOrphan(ctx context.Context, workspaceID foundation.ID, relativePath string) error {
	if err := validateWorkspaceContext(ctx, workspaceID); err != nil {
		return err
	}
	if !validStagingPath(relativePath) {
		return invalid(errors.New("export orphan path is invalid"))
	}
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, false, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	defer managed.Close()
	name := path.Base(relativePath)
	info, err := managed.staging.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return managedBoundaryError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return unsafe(errors.New("export orphan is not a regular private staging file"))
	}
	if err := managed.staging.Remove(name); err != nil {
		return managedBoundaryError(err)
	}
	if err := managed.staging.Sync(); err != nil {
		return managedBoundaryError(err)
	}
	return verifyManagedBindings(managed)
}

func (store *Store) openWorkspaceRoot(ctx context.Context, workspaceID foundation.ID) (*secureRoot, error) {
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
	if store.secureHook.afterWorkspaceLstat != nil {
		store.secureHook.afterWorkspaceLstat()
	}
	root, err := openSecureRoot(rootPath, info, &store.secureHook)
	if err != nil {
		if errors.Is(err, errPathBindingChanged) || errors.Is(err, unix.ELOOP) {
			return nil, unsafe(errors.New("workspace root binding changed while opening"))
		}
		return nil, ioFailure(err)
	}
	return root, nil
}

func (store *Store) openManagedDirectories(ctx context.Context, workspaceID foundation.ID, create, includeStaging bool) (*secureRoot, *managedDirectories, error) {
	root, err := store.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	managed, err := openManagedDirectories(root, create, includeStaging)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		return nil, nil, managedBoundaryError(err)
	}
	return root, managed, nil
}

func managedBoundaryError(err error) error {
	if errors.Is(err, errPathBindingChanged) || errors.Is(err, unix.ELOOP) {
		return unsafe(errors.New("export managed directory binding changed"))
	}
	return ioFailure(err)
}

func managedFileDirectory(managed *managedDirectories, relativePath string) (*secureDir, string) {
	if managed == nil {
		return nil, ""
	}
	if validStagingPath(relativePath) {
		return managed.staging, path.Base(relativePath)
	}
	return managed.exports, path.Base(relativePath)
}

func verifyManagedBindings(managed *managedDirectories) error {
	if err := managed.VerifyBindings(); err != nil {
		return managedBoundaryError(err)
	}
	return nil
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
	return extension == ".md" || extension == ".json" || extension == ".zip"
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

func ensureExportDirectories(root *secureRoot, create bool) error {
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

func ensureStagingDirectory(root *secureRoot, create bool) error {
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

func ensureStagingDirectoryForPromote(root *secureRoot) error {
	err := ensureStagingDirectory(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func ensureManagedDirectories(root *secureRoot, relativePath string, create bool) error {
	if validStagingPath(relativePath) {
		return ensureStagingDirectory(root, create)
	}
	return ensureExportDirectories(root, create)
}

func reserveTemporaryPath(root *secureRoot, target string) (string, error) {
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

func reserveTemporaryName(directory *secureDir, target string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return "", unavailable(err)
		}
		candidate := fmt.Sprintf("%s.%s.tmp", target, hex.EncodeToString(random[:]))
		if _, err := directory.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", managedBoundaryError(err)
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

func verifyExistingInDirectory(ctx context.Context, directory *secureDir, name, expectedHash string, expectedSize int64) error {
	file, err := openVerifiedInDirectory(ctx, directory, name, expectedHash, expectedSize)
	if err != nil {
		return err
	}
	return ioFailureUnlessNil(file.Close())
}

func managedFileExistsInDirectory(directory *secureDir, name string) (bool, error) {
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, managedBoundaryError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, unsafe(errors.New("export target is not a regular file"))
	}
	return true, nil
}

func openVerifiedInDirectory(ctx context.Context, directory *secureDir, name, expectedHash string, expectedSize int64) (*os.File, error) {
	if ctx == nil {
		return nil, invalid(errors.New("export file context is nil"))
	}
	info, err := directory.Lstat(name)
	if err != nil {
		return nil, managedBoundaryError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, unsafe(errors.New("export file is not a regular file"))
	}
	if info.Mode().Perm() != 0o600 || info.Size() != expectedSize {
		return nil, inconsistent(errors.New("export file metadata does not match its binding"))
	}
	file, err := directory.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, managedBoundaryError(err)
	}
	closeWith := func(cause error) (*os.File, error) {
		return nil, errors.Join(cause, ioFailureUnlessNil(file.Close()))
	}
	opened, err := file.Stat()
	if err != nil || !sameFileInfo(info, opened) || !opened.Mode().IsRegular() || linkCount(opened) != 1 || opened.Mode().Perm() != 0o600 || opened.Size() != expectedSize {
		return closeWith(inconsistent(errors.New("export file changed while opening")))
	}
	digest := sha256.New()
	written, err := io.Copy(digest, &contextReader{ctx: ctx, reader: io.LimitReader(file, expectedSize+1)})
	if err != nil {
		return closeWith(ioFailure(err))
	}
	if written != expectedSize || hex.EncodeToString(digest.Sum(nil)) != expectedHash {
		return closeWith(inconsistent(errors.New("export file content does not match its binding")))
	}
	after, err := file.Stat()
	if err != nil || !sameFileInfo(opened, after) || !after.Mode().IsRegular() || linkCount(after) != 1 || after.Size() != expectedSize || after.Mode().Perm() != 0o600 {
		return closeWith(inconsistent(errors.New("export file changed while verifying")))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeWith(ioFailure(err))
	}
	return file, nil
}

func ioFailureUnlessNil(err error) error {
	if err == nil {
		return nil
	}
	return ioFailure(err)
}

func syncDirectory(root *secureRoot, directory string) error {
	if err := root.SyncDirectory(directory); err != nil {
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
