package workspacecontrol

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	grantWorkspaceIDEnv = "ZHIXU_WORKSPACE_GRANTED_ID"
	grantRootEnv        = "ZHIXU_WORKSPACE_GRANTED_ROOT"
	grantGenerationEnv  = "ZHIXU_WORKSPACE_GRANT_GENERATION"
)

var grantIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Grant is the exact host capability applied to both business runtime roles.
type Grant struct {
	WorkspaceID     string
	Root            string
	RootFingerprint string
	BindingVersion  int64
	Generation      int64
}

func (grant Grant) validate() error {
	if !grantIdentityPattern.MatchString(grant.WorkspaceID) {
		return &ValidationError{Code: "WORKSPACE_GRANT_ID_INVALID"}
	}
	if grant.Generation < 1 {
		return &ValidationError{Code: "WORKSPACE_GRANT_GENERATION_INVALID"}
	}
	if grant.Root == "" || !filepath.IsAbs(grant.Root) || filepath.Clean(grant.Root) != grant.Root {
		return &ValidationError{Code: "WORKSPACE_GRANT_ROOT_INVALID"}
	}
	if !fingerprintDigestPattern.MatchString(grant.RootFingerprint) || grant.BindingVersion < 1 {
		return &ValidationError{Code: "WORKSPACE_GRANT_FINGERPRINT_INVALID"}
	}
	if err := validateMountTarget(grant.Root); err != nil {
		return &ValidationError{Code: "WORKSPACE_GRANT_ROOT_RESERVED"}
	}
	return nil
}

// ValidationError identifies a fail-closed grant or Compose invariant.
type ValidationError struct {
	Code string
}

func (validationError *ValidationError) Error() string {
	if validationError == nil || validationError.Code == "" {
		return "workspace grant validation failed"
	}
	return validationError.Code
}

type grantOverride struct {
	Services map[string]grantService `yaml:"services"`
}

type grantService struct {
	Environment map[string]string `yaml:"environment"`
	Volumes     []grantMount      `yaml:"volumes"`
}

type grantMount struct {
	Type   string    `yaml:"type"`
	Source string    `yaml:"source"`
	Target string    `yaml:"target"`
	Bind   grantBind `yaml:"bind"`
}

type grantBind struct {
	CreateHostPath bool `yaml:"create_host_path"`
}

// WriteGrantOverride writes a structured long-syntax Compose override atomically.
func WriteGrantOverride(destination string, grant Grant) error {
	if err := grant.validate(); err != nil {
		return err
	}
	directory := filepath.Dir(destination)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("grant override directory is unavailable")
	}

	environment := map[string]string{
		grantWorkspaceIDEnv: grant.WorkspaceID,
		grantRootEnv:        grant.Root,
		grantGenerationEnv:  fmt.Sprintf("%d", grant.Generation),
	}
	mount := grantMount{
		Type: "bind", Source: grant.Root, Target: grant.Root,
		Bind: grantBind{CreateHostPath: false},
	}
	override := grantOverride{Services: map[string]grantService{
		"app":    {Environment: environment, Volumes: []grantMount{mount}},
		"worker": {Environment: environment, Volumes: []grantMount{mount}},
	}}

	temporary, err := os.CreateTemp(directory, ".workspace-grant-*.tmp")
	if err != nil {
		return errors.New("could not create grant override")
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("could not protect grant override")
	}
	encoder := yaml.NewEncoder(temporary)
	encoder.SetIndent(2)
	if err := encoder.Encode(override); err != nil {
		return errors.New("could not encode grant override")
	}
	if err := encoder.Close(); err != nil {
		return errors.New("could not finalize grant override")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("could not sync grant override")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not close grant override")
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return errors.New("could not publish grant override")
	}
	directoryHandle, err := os.Open(directory)
	if err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	committed = true
	return nil
}

func containsDockerSocket(value string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(value))
	return cleaned == "docker.sock" || strings.Contains(cleaned, "/docker.sock")
}
