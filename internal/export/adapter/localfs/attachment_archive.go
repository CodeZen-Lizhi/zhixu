package localfs

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	attachmentDirectory       = "attachments"
	attachmentSchemaVersion   = "attachment-export/v1"
	attachmentRootVersion     = "workspace-attachments/v1"
	maxAttachmentEntries      = 10_000
	maxAttachmentDirectories  = 10_000
	maxAttachmentFileBytes    = int64(256 * 1024 * 1024)
	maxAttachmentTotalBytes   = int64(1024 * 1024 * 1024)
	maxAttachmentArchiveBytes = int64(1024 * 1024 * 1024)
	attachmentReadDirBatch    = 256
)

var deterministicZIPTime = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
var errAttachmentArchiveLimit = errors.New("attachment archive exceeds its size limit")

type attachmentEntry struct {
	Path       string
	Hash       string
	Size       int64
	Info       os.FileInfo
	ParentPath string
	ParentInfo os.FileInfo
}

type attachmentSnapshot struct {
	Entries              []attachmentEntry
	TotalBytes           int64
	DirectoryCount       int
	DirectoryFingerprint [sha256.Size]byte
}

type attachmentManifest struct {
	SchemaVersion                 string                    `json:"schema_version"`
	WorkspaceID                   string                    `json:"workspace_id"`
	AttachmentRootContractVersion string                    `json:"attachment_root_contract_version"`
	EntryCount                    int64                     `json:"entry_count"`
	TotalUncompressedBytes        int64                     `json:"total_uncompressed_bytes"`
	Entries                       []attachmentManifestEntry `json:"entries"`
}

type attachmentManifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type attachmentArchiveHook struct {
	afterScan               func()
	afterArchive            func()
	beforeFinalVerification func()
}

// StageAttachments 读取固定 attachments/ 根并写入确定性的 create-only staging ZIP。
func (store *Store) StageAttachments(ctx context.Context, workspaceID, exportID foundation.ID) (exportapp.AttachmentArchive, error) {
	if err := validateStageRequest(ctx, workspaceID, exportID, ".zip"); err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	root, managed, err := store.openManagedDirectories(ctx, workspaceID, true, true)
	if err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	defer root.Close()
	defer managed.Close()
	attachments, err := root.openDirectory(attachmentDirectory, false)
	if errors.Is(err, os.ErrNotExist) {
		return exportapp.AttachmentArchive{}, attachmentRootNotFound(errors.New("attachment root does not exist"))
	}
	if err != nil {
		return exportapp.AttachmentArchive{}, unsafe(errors.New("attachment root is not a regular directory"))
	}
	defer attachments.Close()
	snapshot, err := scanAttachmentTree(ctx, root, attachments)
	if err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	entries, totalBytes := snapshot.Entries, snapshot.TotalBytes
	if store.attachmentHook.afterScan != nil {
		store.attachmentHook.afterScan()
	}
	if err := exportapp.AttachmentExportSmokeAfterScan(ctx, exportID); err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	manifestBytes, manifestHash, err := encodeAttachmentManifest(workspaceID, entries, totalBytes)
	if err != nil {
		return exportapp.AttachmentArchive{}, inconsistent(err)
	}
	stagingPath, err := reserveStagingPath(exportID, ".zip")
	if err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	stagingName := path.Base(stagingPath)
	file, err := managed.staging.OpenFile(stagingName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return exportapp.AttachmentArchive{}, managedBoundaryError(err)
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = managed.staging.Remove(stagingName)
		}
	}()

	archiveHasher := sha256.New()
	archiveOutput := &boundedArchiveWriter{writer: io.MultiWriter(file, archiveHasher), remaining: maxAttachmentArchiveBytes}
	archive := zip.NewWriter(archiveOutput)
	writeErr := writeZIPEntry(archive, "manifest.json", manifestBytes)
	for _, entry := range entries {
		if writeErr != nil {
			break
		}
		writeErr = copyAttachmentEntry(ctx, root, archive, entry)
	}
	closeArchiveErr := archive.Close()
	syncErr := file.Sync()
	closeFileErr := file.Close()
	if writeErr != nil {
		return exportapp.AttachmentArchive{}, writeErr
	}
	if closeArchiveErr != nil || syncErr != nil || closeFileErr != nil {
		return exportapp.AttachmentArchive{}, archiveWriteFailure(errors.Join(closeArchiveErr, syncErr, closeFileErr))
	}
	info, err := managed.staging.Lstat(stagingName)
	if err != nil {
		return exportapp.AttachmentArchive{}, managedBoundaryError(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 {
		return exportapp.AttachmentArchive{}, inconsistent(errors.New("attachment archive violates its result contract"))
	}
	if info.Size() > maxAttachmentArchiveBytes {
		return exportapp.AttachmentArchive{}, attachmentLimitExceeded(errAttachmentArchiveLimit)
	}
	archiveHash := hex.EncodeToString(archiveHasher.Sum(nil))
	if store.attachmentHook.afterArchive != nil {
		store.attachmentHook.afterArchive()
	}
	if err := verifyManagedBindings(managed); err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	if err := root.assertDirectoryBinding(attachmentDirectory, attachments.info); err != nil {
		return exportapp.AttachmentArchive{}, inconsistent(errors.New("attachment root changed while archiving"))
	}
	if err := managed.staging.Sync(); err != nil {
		return exportapp.AttachmentArchive{}, managedBoundaryError(err)
	}
	if err := verifyExistingInDirectory(ctx, managed.staging, stagingName, archiveHash, info.Size()); err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	if err := verifyManagedBindings(managed); err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	if err := root.assertDirectoryBinding(attachmentDirectory, attachments.info); err != nil {
		return exportapp.AttachmentArchive{}, inconsistent(errors.New("attachment root changed while finalizing archive"))
	}
	if store.attachmentHook.beforeFinalVerification != nil {
		store.attachmentHook.beforeFinalVerification()
	}
	finalSnapshot, err := scanAttachmentTree(ctx, root, attachments)
	if err != nil {
		return exportapp.AttachmentArchive{}, err
	}
	if !sameAttachmentSnapshot(snapshot, finalSnapshot) {
		return exportapp.AttachmentArchive{}, inconsistent(errors.New("attachment source changed while finalizing archive"))
	}
	if err := root.assertDirectoryBinding(attachmentDirectory, attachments.info); err != nil {
		return exportapp.AttachmentArchive{}, inconsistent(errors.New("attachment root changed while finalizing archive"))
	}
	removeStaging = false
	return exportapp.AttachmentArchive{
		PreparedFile: exportapp.PreparedFile{
			StagingPath: stagingPath,
			FinalPath:   finalPathFor(exportID, ".zip"),
			FileHash:    archiveHash,
			FileSize:    info.Size(),
		},
		ManifestHash: manifestHash, EntryCount: int64(len(entries)), TotalUncompressedBytes: totalBytes,
	}, nil
}

func scanAttachmentTree(ctx context.Context, root *secureRoot, attachments *secureDir) (attachmentSnapshot, error) {
	if attachments == nil || attachments.info.Mode()&os.ModeSymlink != 0 || !attachments.info.IsDir() {
		return attachmentSnapshot{}, unsafe(errors.New("attachment root is not a regular directory"))
	}
	snapshot := attachmentSnapshot{Entries: make([]attachmentEntry, 0)}
	keys := make(map[string]string)
	var walk func(*secureDir, string) error
	walk = func(directory *secureDir, relativeDirectory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return directory.ForEachDirEntry(attachmentReadDirBatch, func(child os.DirEntry) error {
			childInfo, err := directory.Lstat(child.Name())
			if err != nil {
				return ioFailure(err)
			}
			if childInfo.Mode()&os.ModeSymlink != 0 {
				return unsafe(errors.New("attachment tree contains a symbolic link"))
			}
			if childInfo.IsDir() {
				if snapshot.DirectoryCount >= maxAttachmentDirectories {
					return attachmentLimitExceeded(errors.New("attachment export exceeds its directory limit"))
				}
				relative := path.Join(relativeDirectory, child.Name())
				if err := validateAttachmentRelativePath(relative, true); err != nil {
					return err
				}
				if err := recordAttachmentPath(keys, relative); err != nil {
					return err
				}
				childDirectory, err := directory.OpenDirectory(child.Name())
				if err != nil {
					if errors.Is(err, errPathBindingChanged) {
						return inconsistent(errors.New("attachment directory changed while opening"))
					}
					return unsafe(errors.New("attachment directory changed while opening"))
				}
				if !sameFileInfo(childInfo, childDirectory.info) {
					_ = childDirectory.Close()
					return inconsistent(errors.New("attachment directory changed while opening"))
				}
				if err := addAttachmentDirectory(&snapshot, relative, childDirectory.info); err != nil {
					_ = childDirectory.Close()
					return err
				}
				walkErr := walk(childDirectory, relative)
				closeErr := childDirectory.Close()
				if walkErr != nil {
					return walkErr
				}
				if closeErr != nil {
					return ioFailure(closeErr)
				}
				if err := root.assertDirectoryBinding(path.Join(attachmentDirectory, relative), childInfo); err != nil {
					return inconsistent(errors.New("attachment directory changed while scanning"))
				}
				if err := root.assertDirectoryBinding(attachmentDirectory, attachments.info); err != nil {
					return inconsistent(errors.New("attachment root changed while scanning"))
				}
				return nil
			}
			if !childInfo.Mode().IsRegular() || linkCount(childInfo) != 1 {
				return unsafe(errors.New("attachment tree contains an unsupported file type"))
			}
			relative := path.Join(relativeDirectory, child.Name())
			if err := validateAttachmentRelativePath(relative, false); err != nil {
				return err
			}
			if directory.hook != nil && directory.hook.afterFileLstat != nil {
				directory.hook.afterFileLstat(relative)
			}
			if err := recordAttachmentPath(keys, relative); err != nil {
				return err
			}
			if err := validateAttachmentLimits(len(snapshot.Entries), snapshot.TotalBytes, childInfo.Size()); err != nil {
				return err
			}
			hash, verifiedInfo, err := hashAttachmentFile(ctx, directory, child.Name(), childInfo)
			if err != nil {
				return err
			}
			snapshot.TotalBytes += childInfo.Size()
			snapshot.Entries = append(snapshot.Entries, attachmentEntry{Path: relative, Hash: hash, Size: childInfo.Size(), Info: verifiedInfo,
				ParentPath: path.Join(attachmentDirectory, relativeDirectory), ParentInfo: directory.info})
			return nil
		})
	}
	if err := walk(attachments, ""); err != nil {
		return attachmentSnapshot{}, err
	}
	sort.Slice(snapshot.Entries, func(left, right int) bool {
		return norm.NFC.String(snapshot.Entries[left].Path) < norm.NFC.String(snapshot.Entries[right].Path)
	})
	return snapshot, nil
}

func addAttachmentDirectory(snapshot *attachmentSnapshot, relative string, info os.FileInfo) error {
	device, inode, ok := fileIdentity(info)
	if snapshot == nil || !ok {
		return inconsistent(errors.New("attachment directory identity is unavailable"))
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(norm.NFC.String(relative)))
	_, _ = digest.Write([]byte{0})
	var identity [16]byte
	binary.BigEndian.PutUint64(identity[:8], device)
	binary.BigEndian.PutUint64(identity[8:], inode)
	_, _ = digest.Write(identity[:])
	record := digest.Sum(nil)
	for index := range snapshot.DirectoryFingerprint {
		snapshot.DirectoryFingerprint[index] ^= record[index]
	}
	snapshot.DirectoryCount++
	return nil
}

func validateAttachmentLimits(entryCount int, totalBytes, fileBytes int64) error {
	if fileBytes < 0 || fileBytes > maxAttachmentFileBytes || totalBytes < 0 || totalBytes > maxAttachmentTotalBytes-fileBytes {
		return attachmentLimitExceeded(errors.New("attachment export exceeds its size limit"))
	}
	if entryCount >= maxAttachmentEntries {
		return attachmentLimitExceeded(errors.New("attachment export exceeds its entry limit"))
	}
	return nil
}

func recordAttachmentPath(seen map[string]string, relative string) error {
	normalized := norm.NFC.String(relative)
	for _, key := range []string{relative, normalized, cases.Fold().String(normalized)} {
		if previous, exists := seen[key]; exists && previous != relative {
			return invalid(errors.New("attachment paths collide after normalization"))
		}
		seen[key] = relative
	}
	return nil
}

func hashAttachmentFile(ctx context.Context, parent *secureDir, name string, expected os.FileInfo) (string, os.FileInfo, error) {
	file, err := parent.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", nil, ioFailure(err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || linkCount(opened) != 1 || !sameFileInfo(expected, opened) {
		return "", nil, inconsistent(errors.New("attachment identity changed while opening"))
	}
	digest := sha256.New()
	written, err := io.Copy(digest, &contextReader{ctx: ctx, reader: io.LimitReader(file, maxAttachmentFileBytes+1)})
	if err != nil {
		return "", nil, ioFailure(err)
	}
	if written != expected.Size() {
		return "", nil, inconsistent(errors.New("attachment size changed while hashing"))
	}
	after, err := file.Stat()
	if err != nil || !sameFileInfo(opened, after) || after.Size() != expected.Size() {
		return "", nil, inconsistent(errors.New("attachment changed while hashing"))
	}
	return hex.EncodeToString(digest.Sum(nil)), after, nil
}

func copyAttachmentEntry(ctx context.Context, root *secureRoot, archive *zip.Writer, entry attachmentEntry) error {
	parent, err := root.openDirectory(entry.ParentPath, false)
	if err != nil {
		return inconsistent(errors.New("attachment parent changed while archiving"))
	}
	defer parent.Close()
	if !sameFileInfo(parent.info, entry.ParentInfo) {
		return inconsistent(errors.New("attachment parent changed while archiving"))
	}
	file, err := parent.OpenFile(path.Base(entry.Path), os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return ioFailure(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || linkCount(info) != 1 || !sameFileInfo(entry.Info, info) || info.Size() != entry.Size {
		return inconsistent(errors.New("attachment identity changed while archiving"))
	}
	header := deterministicZIPHeader("attachments/"+entry.Path, entry.Size)
	writer, err := archive.CreateHeader(&header)
	if err != nil {
		return archiveWriteFailure(err)
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(writer, digest), &contextReader{ctx: ctx, reader: io.LimitReader(file, entry.Size+1)})
	if err != nil {
		return archiveWriteFailure(err)
	}
	if written != entry.Size || hex.EncodeToString(digest.Sum(nil)) != entry.Hash {
		return inconsistent(errors.New("attachment changed while archiving"))
	}
	return nil
}

func encodeAttachmentManifest(workspaceID foundation.ID, entries []attachmentEntry, totalBytes int64) ([]byte, string, error) {
	manifest := attachmentManifest{
		SchemaVersion: attachmentSchemaVersion, WorkspaceID: string(workspaceID),
		AttachmentRootContractVersion: attachmentRootVersion,
		EntryCount:                    int64(len(entries)), TotalUncompressedBytes: totalBytes,
		Entries: make([]attachmentManifestEntry, len(entries)),
	}
	for index, entry := range entries {
		manifest.Entries[index] = attachmentManifestEntry{Path: entry.Path, SHA256: entry.Hash, Size: entry.Size}
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", err
	}
	payload = append(payload, '\n')
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

func writeZIPEntry(archive *zip.Writer, name string, payload []byte) error {
	header := deterministicZIPHeader(name, int64(len(payload)))
	writer, err := archive.CreateHeader(&header)
	if err != nil {
		return archiveWriteFailure(err)
	}
	_, err = writer.Write(payload)
	return archiveWriteFailure(err)
}

func deterministicZIPHeader(name string, size int64) zip.FileHeader {
	header := zip.FileHeader{Name: name, Method: zip.Store, Flags: 0x800}
	header.SetModTime(deterministicZIPTime)
	// Keep the fixed DOS timestamp while preventing archive/zip from emitting
	// the optional extended timestamp extra field.
	header.Modified = time.Time{}
	header.SetMode(0o600)
	header.UncompressedSize64 = uint64(size)
	header.Extra = nil
	header.Comment = ""
	return header
}

type boundedArchiveWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *boundedArchiveWriter) Write(payload []byte) (int, error) {
	if writer == nil || writer.writer == nil || writer.remaining < 0 || int64(len(payload)) > writer.remaining {
		return 0, errAttachmentArchiveLimit
	}
	written, err := writer.writer.Write(payload)
	writer.remaining -= int64(written)
	return written, err
}

func archiveWriteFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAttachmentArchiveLimit) {
		return attachmentLimitExceeded(err)
	}
	return ioFailure(err)
}

func attachmentRootNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeAttachmentRootNotFound, false, cause)
}

func attachmentLimitExceeded(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeAttachmentLimitExceeded, false, cause)
}

func validateAttachmentRelativePath(value string, directory bool) error {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) || path.Clean(value) != value {
		return invalid(errors.New("attachment path is invalid"))
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return invalid(errors.New("attachment path is invalid"))
		}
	}
	if directory && strings.HasSuffix(value, "/") {
		return invalid(errors.New("attachment directory path is invalid"))
	}
	return nil
}

func sameAttachmentSnapshot(left, right attachmentSnapshot) bool {
	if left.TotalBytes != right.TotalBytes || len(left.Entries) != len(right.Entries) ||
		left.DirectoryCount != right.DirectoryCount || left.DirectoryFingerprint != right.DirectoryFingerprint {
		return false
	}
	for index := range left.Entries {
		if left.Entries[index].Path != right.Entries[index].Path || left.Entries[index].Hash != right.Entries[index].Hash || left.Entries[index].Size != right.Entries[index].Size ||
			!sameFileInfo(left.Entries[index].Info, right.Entries[index].Info) || left.Entries[index].ParentPath != right.Entries[index].ParentPath || !sameFileInfo(left.Entries[index].ParentInfo, right.Entries[index].ParentInfo) {
			return false
		}
	}
	return true
}

func linkCount(info os.FileInfo) uint64 {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0
	}
	count, ok := reflectUnsigned(field)
	if !ok {
		return 0
	}
	return count
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
