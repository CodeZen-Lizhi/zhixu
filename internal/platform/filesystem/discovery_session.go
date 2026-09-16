package filesystem

import (
	"context"
	"errors"
	"os"

	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type discoverySession struct {
	Scanner
	rootPath string
	validate func(context.Context) error
}

func (s Scanner) BindSourceDiscovery(ctx context.Context, rootPath string, validate func(context.Context) error) (domain.SourceDiscoverySession, error) {
	if validate == nil {
		return nil, errors.New("root validator unavailable")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	session := &discoverySession{Scanner: s, rootPath: rootPath, validate: validate}
	session.Scanner.boundRoot = root
	if err := session.Revalidate(ctx); err != nil {
		_ = root.Close()
		return nil, err
	}
	return session, nil
}
func (s *discoverySession) Revalidate(ctx context.Context) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	opened, err := s.boundRoot.Stat(".")
	if err != nil {
		return err
	}
	live, err := os.Lstat(s.rootPath)
	if err != nil {
		return err
	}
	if !live.IsDir() || !os.SameFile(opened, live) {
		return errors.New("authorized discovery root changed")
	}
	return ctx.Err()
}
func (s *discoverySession) Close() error { return s.boundRoot.Close() }

// 借用的根目录在整个发现会话期间保持打开。
func (s Scanner) discoveryRoot(path string) (*os.Root, func(), error) {
	if s.boundRoot != nil {
		return s.boundRoot, func() {}, nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, func() {}, err
	}
	return root, func() { _ = root.Close() }, nil
}
