package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const workspaceAnalysisToolCatalogSnapshotSchemaVersion int64 = 1

var workspaceAnalysisToolCatalogHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var workspaceAnalysisToolCatalogRefs = []domain.ToolRef{
	{Name: "ReadGitStatus", Version: toolVersionV2},
	{Name: "SearchKnowledge", Version: toolVersionV2},
	{Name: "ReadSource", Version: toolVersionV3},
	{Name: "ValidateCitation", Version: toolVersionV3},
}

var workspaceAnalysisToolCatalogExpectedTimeouts = map[domain.ToolRef]time.Duration{
	{Name: "ReadGitStatus", Version: toolVersionV2}:    10 * time.Second,
	{Name: "SearchKnowledge", Version: toolVersionV2}:  localReadTimeout,
	{Name: "ReadSource", Version: toolVersionV3}:       localReadTimeout,
	{Name: "ValidateCitation", Version: toolVersionV3}: localReadTimeout,
}

// WorkspaceAnalysisToolCatalog 冻结 Workspace Analysis v1 的精确 Tool 目录与单次超时。
// Hash 由 SchemaVersion 和按既定顺序排列的 Tools 的 canonical JSON 计算得出。
type WorkspaceAnalysisToolCatalog struct {
	SchemaVersion           int64                               `json:"schema_version"`
	Tools                   []WorkspaceAnalysisToolCatalogEntry `json:"tools"`
	Hash                    string                              `json:"hash"`
	ReadGitStatusTimeout    time.Duration                       `json:"-"`
	SearchKnowledgeTimeout  time.Duration                       `json:"-"`
	ReadSourceTimeout       time.Duration                       `json:"-"`
	ValidateCitationTimeout time.Duration                       `json:"-"`
}

// WorkspaceAnalysisToolCatalogEntry 标识纳入 Workspace Analysis v1 snapshot 的精确 Tool 定义。
type WorkspaceAnalysisToolCatalogEntry struct {
	Name           string `json:"name"`
	Version        int64  `json:"version"`
	DefinitionHash string `json:"definition_hash"`
}

type workspaceAnalysisToolCatalogCanonicalDocument struct {
	SchemaVersion int64                               `json:"schema_version"`
	Tools         []WorkspaceAnalysisToolCatalogEntry `json:"tools"`
}

type workspaceAnalysisContractResolver interface {
	ResolveContract(domain.ToolRef) (application.Contract, error)
}

// WorkspaceAnalysisToolCatalogSnapshotFromRegistry 从已冻结 contract Registry 生成并验证 v1 Tool snapshot。
func WorkspaceAnalysisToolCatalogSnapshotFromRegistry(registry *application.Registry) (WorkspaceAnalysisToolCatalog, error) {
	if registry == nil {
		return WorkspaceAnalysisToolCatalog{}, errors.New("workspace analysis tool catalog registry is nil")
	}
	return workspaceAnalysisToolCatalogSnapshot(registry)
}

// WorkspaceAnalysisToolCatalogSnapshot 从内置冻结 contracts 生成并验证 v1 Tool snapshot。
func WorkspaceAnalysisToolCatalogSnapshot() (WorkspaceAnalysisToolCatalog, error) {
	registry, err := NewFrozenContractRegistry()
	if err != nil {
		return WorkspaceAnalysisToolCatalog{}, fmt.Errorf("create frozen tool registry: %w", err)
	}
	return workspaceAnalysisToolCatalogSnapshot(registry)
}

func workspaceAnalysisToolCatalogSnapshot(registry workspaceAnalysisContractResolver) (WorkspaceAnalysisToolCatalog, error) {
	return workspaceAnalysisToolCatalogSnapshotVersion(registry, 1)
}

func workspaceAnalysisToolCatalogSnapshotVersion(registry workspaceAnalysisContractResolver, version int64) (WorkspaceAnalysisToolCatalog, error) {
	if registry == nil {
		return WorkspaceAnalysisToolCatalog{}, errors.New("workspace analysis tool catalog registry is nil")
	}

	snapshot := WorkspaceAnalysisToolCatalog{
		SchemaVersion: version,
		Tools:         make([]WorkspaceAnalysisToolCatalogEntry, 0, len(workspaceAnalysisToolCatalogRefs)),
	}
	for _, ref := range workspaceAnalysisToolCatalogRefs {
		if version == 2 {
			ref.Version++
		}
		contract, err := registry.ResolveContract(ref)
		if err != nil {
			return WorkspaceAnalysisToolCatalog{}, fmt.Errorf("resolve workspace analysis tool %s@%d: %w", ref.Name, ref.Version, err)
		}
		if err := validateWorkspaceAnalysisToolCatalogContractVersion(ref, contract, version); err != nil {
			return WorkspaceAnalysisToolCatalog{}, err
		}
		snapshot.Tools = append(snapshot.Tools, WorkspaceAnalysisToolCatalogEntry{
			Name: ref.Name, Version: ref.Version, DefinitionHash: contract.Definition.DefinitionHash,
		})
		switch ref.Name {
		case "ReadGitStatus":
			snapshot.ReadGitStatusTimeout = contract.Definition.Timeout
		case "SearchKnowledge":
			snapshot.SearchKnowledgeTimeout = contract.Definition.Timeout
		case "ReadSource":
			snapshot.ReadSourceTimeout = contract.Definition.Timeout
		case "ValidateCitation":
			snapshot.ValidateCitationTimeout = contract.Definition.Timeout
		}
	}

	canonical, err := json.Marshal(workspaceAnalysisToolCatalogCanonicalDocument{
		SchemaVersion: snapshot.SchemaVersion,
		Tools:         snapshot.Tools,
	})
	if err != nil {
		return WorkspaceAnalysisToolCatalog{}, fmt.Errorf("encode workspace analysis tool catalog snapshot: %w", err)
	}
	digest := sha256.Sum256(canonical)
	snapshot.Hash = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func validateWorkspaceAnalysisToolCatalogContract(ref domain.ToolRef, contract application.Contract) error {
	return validateWorkspaceAnalysisToolCatalogContractVersion(ref, contract, 1)
}

func validateWorkspaceAnalysisToolCatalogContractVersion(ref domain.ToolRef, contract application.Contract, version int64) error {
	definition := contract.Definition
	if definition.Ref != ref {
		return fmt.Errorf("workspace analysis tool %s@%d reference drifted", ref.Name, ref.Version)
	}
	canonical, err := domain.CanonicalizeDefinition(definition)
	if err != nil || canonical.DefinitionHash != definition.DefinitionHash ||
		!workspaceAnalysisToolCatalogHashPattern.MatchString(definition.DefinitionHash) {
		return fmt.Errorf("workspace analysis tool %s@%d definition hash drifted", ref.Name, ref.Version)
	}
	timeoutRef := ref
	if version == 2 {
		timeoutRef.Version--
	}
	expectedTimeout, found := workspaceAnalysisToolCatalogExpectedTimeouts[timeoutRef]
	if !found {
		return fmt.Errorf("workspace analysis tool %s@%d timeout contract is missing", ref.Name, ref.Version)
	}
	receipt, found := domain.WorkspaceAnalysisResultReceiptContract(ref)
	if !found || definition.OutputSchema != receipt.OutputSchema || definition.MaxOutputBytes != receipt.MaxOutputBytes ||
		definition.RequiredCapability != capability.ReadLocal || definition.SideEffectLevel != domain.SideEffectNone ||
		definition.InvocationPolicy != domain.InvocationTrustedWorkflowOnly ||
		definition.ResultPersistencePolicy != domain.ResultPersistenceCanonical || definition.IdempotencyMode != domain.IdempotencyNone ||
		definition.RetryPolicy.MaxAttempts != 1 || definition.Timeout != expectedTimeout || definition.Timeout > domain.MaxToolTimeout ||
		len(definition.AllowedWorkflows) != 1 ||
		definition.AllowedWorkflows[0] != (domain.WorkflowBinding{Key: workspaceAnalysisFlow, Version: version}) {
		return fmt.Errorf("workspace analysis tool %s@%d contract drifted", ref.Name, ref.Version)
	}
	return nil
}
