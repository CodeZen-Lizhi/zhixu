package workspacecontrol

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type composeModel struct {
	Services map[string]composeService `json:"services"`
}

type composeService struct {
	Environment map[string]string `json:"environment"`
	Volumes     []composeMount    `json:"volumes"`
	User        string            `json:"user"`
}

type composeMount struct {
	Type     string       `json:"type"`
	Source   string       `json:"source"`
	Target   string       `json:"target"`
	ReadOnly bool         `json:"read_only"`
	Bind     *composeBind `json:"bind"`
}

type composeBind struct {
	CreateHostPath bool `json:"create_host_path"`
}

// ValidateBaseComposeModel proves that the checked-in model grants no host Workspace.
func ValidateBaseComposeModel(document []byte) error {
	model, err := decodeComposeModel(document)
	if err != nil {
		return err
	}
	for _, service := range model.Services {
		for _, key := range []string{grantWorkspaceIDEnv, grantRootEnv, grantGenerationEnv, "ZHIXU_WORKSPACE_ROOT"} {
			if _, found := service.Environment[key]; found {
				return &ValidationError{Code: "BASE_WORKSPACE_GRANT_PRESENT"}
			}
		}
		for _, mount := range service.Volumes {
			if containsDockerSocket(mount.Source) || containsDockerSocket(mount.Target) {
				return &ValidationError{Code: "DOCKER_SOCKET_MOUNT_FORBIDDEN"}
			}
			if mount.Type == "bind" {
				return &ValidationError{Code: "BASE_BIND_MOUNT_FORBIDDEN"}
			}
		}
	}
	return nil
}

// ValidateGrantedComposeModel proves exact source==target parity for API and Worker.
func ValidateGrantedComposeModel(document []byte, grant Grant) error {
	if err := grant.validate(); err != nil {
		return err
	}
	model, err := decodeComposeModel(document)
	if err != nil {
		return err
	}
	for _, role := range []string{"app", "worker"} {
		service, found := model.Services[role]
		if !found {
			return &ValidationError{Code: "WORKSPACE_RUNTIME_SERVICE_MISSING"}
		}
		if service.User != "10001:10001" {
			return &ValidationError{Code: "WORKSPACE_RUNTIME_USER_INVALID"}
		}
		if service.Environment[grantWorkspaceIDEnv] != grant.WorkspaceID ||
			service.Environment[grantRootEnv] != grant.Root ||
			service.Environment[grantGenerationEnv] != strconv.FormatInt(grant.Generation, 10) {
			return &ValidationError{Code: "WORKSPACE_GRANT_ENV_MISMATCH"}
		}
		if _, legacy := service.Environment["ZHIXU_WORKSPACE_ROOT"]; legacy {
			return &ValidationError{Code: "LEGACY_WORKSPACE_ROOT_FORBIDDEN"}
		}
		binds := bindMounts(service.Volumes)
		if len(binds) != 1 {
			return &ValidationError{Code: "WORKSPACE_BIND_COUNT_INVALID"}
		}
		mount := binds[0]
		if mount.Source != grant.Root || mount.Target != grant.Root || mount.Source != mount.Target {
			return &ValidationError{Code: "WORKSPACE_BIND_IDENTITY_MISMATCH"}
		}
		if mount.ReadOnly || mount.Bind == nil || mount.Bind.CreateHostPath {
			return &ValidationError{Code: "WORKSPACE_BIND_POLICY_INVALID"}
		}
		if err := validateMountTarget(mount.Target); err != nil {
			return &ValidationError{Code: "WORKSPACE_BIND_TARGET_RESERVED"}
		}
	}

	for serviceName, service := range model.Services {
		if serviceName != "app" && serviceName != "worker" {
			for _, key := range []string{grantWorkspaceIDEnv, grantRootEnv, grantGenerationEnv, "ZHIXU_WORKSPACE_ROOT"} {
				if _, found := service.Environment[key]; found {
					return &ValidationError{Code: "NON_RUNTIME_WORKSPACE_GRANT_FORBIDDEN"}
				}
			}
		}
		for _, mount := range service.Volumes {
			if containsDockerSocket(mount.Source) || containsDockerSocket(mount.Target) {
				return &ValidationError{Code: "DOCKER_SOCKET_MOUNT_FORBIDDEN"}
			}
			if serviceName != "app" && serviceName != "worker" && mount.Type == "bind" {
				return &ValidationError{Code: "NON_RUNTIME_BIND_MOUNT_FORBIDDEN"}
			}
		}
	}
	return nil
}

func decodeComposeModel(document []byte) (composeModel, error) {
	var model composeModel
	if err := json.Unmarshal(document, &model); err != nil || model.Services == nil {
		return composeModel{}, &ValidationError{Code: "COMPOSE_MODEL_INVALID"}
	}
	return model, nil
}

func bindMounts(mounts []composeMount) []composeMount {
	result := make([]composeMount, 0, 1)
	for _, mount := range mounts {
		if mount.Type == "bind" {
			result = append(result, mount)
		}
	}
	return result
}

func validationCode(err error) string {
	if validationError, ok := err.(*ValidationError); ok {
		return validationError.Code
	}
	return fmt.Sprintf("%T", err)
}
