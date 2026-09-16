package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// ScanPage 使用既有的格式/大小策略和 os.Root 边界。
// 它绝不跟随目录符号链接。个别异常条目在下一轮
// 重试，不阻塞工作区其余部分。
func (s Scanner) ScanPage(ctx context.Context, rootPath, after string, limit int) (domain.SourceDiscoveryPaths, error) {
	result := domain.SourceDiscoveryPaths{After: after}
	if ctx == nil || limit < 1 || limit > 100 || (after != "" && (!fs.ValidPath(after) || after == ".")) || s.Options.MaxBytes < 0 {
		return result, errors.New("invalid source discovery page")
	}
	root, closeRoot, err := s.discoveryRoot(rootPath)
	if err != nil {
		return result, err
	}
	defer closeRoot()
	identity, err := root.Stat(".")
	if err != nil {
		return result, err
	}
	allowed := s.Options.AllowedExtensions
	if len(allowed) == 0 {
		allowed = DefaultExtensions()
	}
	visited := 0
	stopped := false
	readDirectories := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == "." {
			return walkErr
		}
		if entry != nil && entry.IsDir() {
			switch entry.Name() {
			case ".git", ".knowledge", "tmp", ".tmp":
				return fs.SkipDir
			}
		}
		if !domain.ValidDiscoveryPath(relative) {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// ReadDir 会对目录触发第二次回调。应在游标和分页
		// 限制前处理，包括恢复遍历时经过的祖先目录。
		if walkErr != nil {
			delete(readDirectories, relative)
			result.Failed++
			result.Failures = append(result.Failures, domain.DiscoveryObservation{Path: relative, Stage: "WALK", Code: "DIRECTORY_READ_FAILED"})
			return nil
		}
		if after != "" && slices.Compare(strings.Split(relative, "/"), strings.Split(after, "/")) <= 0 {
			if entry != nil && entry.IsDir() && relative != after && !strings.HasPrefix(after, relative+"/") {
				return fs.SkipDir
			}
			return nil
		}
		if visited == limit {
			stopped = true
			return fs.SkipAll
		}
		visited++
		result.After = relative
		if entry.IsDir() {
			readDirectories[relative] = true
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		extension := strings.ToLower(path.Ext(relative))
		if _, ok := allowed[extension]; !ok {
			return nil
		}
		result.Paths = append(result.Paths, relative)
		return nil
	})
	if err != nil {
		return domain.SourceDiscoveryPaths{After: after}, err
	}
	live, err := os.Stat(rootPath)
	if err != nil {
		return domain.SourceDiscoveryPaths{After: after}, err
	}
	if !os.SameFile(identity, live) {
		return domain.SourceDiscoveryPaths{After: after}, errors.New("workspace root identity changed")
	}
	for relative := range readDirectories {
		if domain.ValidDiscoveryPath(relative) {
			result.ReadDirectories = append(result.ReadDirectories, relative)
		}
	}
	slices.Sort(result.ReadDirectories)
	result.Done = !stopped
	return result, nil
}

// ObserveSource 在既有的不可变采集前立即计算路径指纹。
// 注册时重新检查哈希，因此变化中的文件会在稍后重试。
func (s Scanner) ObserveSource(ctx context.Context, rootPath, relative string) (domain.ScannedFile, error) {
	if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\:\x00") {
		return domain.ScannedFile{}, errors.New("invalid discovery path")
	}
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case ".git", ".knowledge", "tmp", ".tmp":
			return domain.ScannedFile{}, errors.New("excluded discovery path")
		}
	}
	root, closeRoot, err := s.discoveryRoot(rootPath)
	if err != nil {
		return domain.ScannedFile{}, err
	}
	defer closeRoot()
	maxBytes := s.Options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	return observeDiscoveryFile(ctx, root, relative, strings.ToLower(path.Ext(relative)), maxBytes)
}

func observeDiscoveryFile(ctx context.Context, root *os.Root, relative, extension string, maxBytes int64) (domain.ScannedFile, error) {
	var result domain.ScannedFile
	file, err := root.Open(relative)
	if err != nil {
		return result, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxBytes {
		return result, errors.New("source is not a supported regular file")
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, readErr := file.Read(buffer)
		total += int64(n)
		if total > maxBytes {
			return result, errors.New("source exceeds size limit")
		}
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return result, readErr
		}
	}
	after, err := file.Stat()
	if err != nil {
		return result, err
	}
	if total != before.Size() || after.Size() != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return result, errors.New("source changed during observation")
	}
	return domain.ScannedFile{RelativePath: relative, ByteSize: total, ContentHash: hex.EncodeToString(hash.Sum(nil)), MediaType: mediaType(extension)}, nil
}

var _ domain.SourceDiscoveryScanner = Scanner{}
