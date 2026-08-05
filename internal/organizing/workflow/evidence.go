package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	maxVerifiedEvidence       = 500
	maxCitationsPerSection    = 32
	maxRenderedExcerptBytes   = 4096
	evidenceUnavailableGap    = "EVIDENCE_UNAVAILABLE"
	evidenceCapacityGap       = "EVIDENCE_CAPACITY_LIMIT"
	evidenceUnavailableDetail = "没有可用于本章节的已验证正式知识证据。"
	evidenceCapacityDetail    = "部分冻结证据超出单次受控生成容量，未写入本章节。"
)

// CitationVerifier is the Artifact owner seam that reopens formal evidence.
type CitationVerifier interface {
	VerifyCitations(context.Context, foundation.ID, []artifactapp.CitationInput) ([]artifactdomain.Citation, error)
}

// EvidenceRenderer turns frozen citation identities into deterministic,
// evidence-only Artifact sections. It never invents unsupported prose.
type EvidenceRenderer struct{ verifier CitationVerifier }

// NewEvidenceRenderer constructs a fail-closed frozen-evidence renderer.
func NewEvidenceRenderer(verifier CitationVerifier) (*EvidenceRenderer, error) {
	if verifier == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_EVIDENCE_RENDERER_UNAVAILABLE", true, "artifact citation verifier is unavailable")
	}
	return &EvidenceRenderer{verifier: verifier}, nil
}

type verifiedEvidence struct {
	input    artifactapp.CitationInput
	citation artifactdomain.Citation
}

// Render builds every declared section and preserves explicit GAPs.
func (renderer *EvidenceRenderer) Render(ctx context.Context, snapshot organizingdomain.Snapshot, revision organizingdomain.TemplateRevision) ([]artifactapp.SectionInput, error) {
	if renderer == nil || renderer.verifier == nil || ctx == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_EVIDENCE_RENDERER_UNAVAILABLE", true, "organizing evidence renderer is unavailable")
	}
	evidence, capacityLimited, err := renderer.openFrozenEvidence(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return renderTemplateSections(revision, evidence, capacityLimited), nil
}

// RenderMerge renders the fixed merge template and returns its deterministic
// comparison facts for the subsequent human-review receipt.
func (renderer *EvidenceRenderer) RenderMerge(ctx context.Context, snapshot organizingdomain.Snapshot, revision organizingdomain.TemplateRevision) ([]artifactapp.SectionInput, mergeComparison, error) {
	if renderer == nil || renderer.verifier == nil || ctx == nil {
		return nil, mergeComparison{}, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_EVIDENCE_RENDERER_UNAVAILABLE", true, "organizing evidence renderer is unavailable")
	}
	evidence, capacityLimited, err := renderer.openFrozenEvidence(ctx, snapshot)
	if err != nil {
		return nil, mergeComparison{}, err
	}
	comparison := classifyMergeEvidence(evidence)
	return renderMergeSections(revision, evidence, comparison, capacityLimited), comparison, nil
}

func (renderer *EvidenceRenderer) openFrozenEvidence(ctx context.Context, snapshot organizingdomain.Snapshot) ([]verifiedEvidence, bool, error) {
	type requestedEvidence struct {
		input  artifactapp.CitationInput
		frozen organizingdomain.EvidenceRef
	}
	requested := make([]requestedEvidence, 0)
	seen := make(map[string]struct{})
	capacityLimited := false
	for _, material := range snapshot.Materials {
		for _, frozen := range material.Evidence {
			// Artifact Citation identity is Source Version + Span. Deduplicating at
			// that owner boundary also prevents one span reached through two indexes
			// from being represented twice in a section.
			key := string(frozen.SourceVersionID) + "\x00" + string(frozen.SourceSpanID)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if len(requested) == maxVerifiedEvidence {
				capacityLimited = true
				continue
			}
			requested = append(requested, requestedEvidence{
				input: artifactapp.CitationInput{
					IndexVersionID: frozen.IndexVersionID, ChunkID: frozen.ChunkID,
					SourceVersionID: frozen.SourceVersionID, SourceSpanID: frozen.SourceSpanID,
				},
				frozen: frozen,
			})
		}
	}
	if len(requested) == 0 {
		return []verifiedEvidence{}, capacityLimited, nil
	}
	inputs := make([]artifactapp.CitationInput, len(requested))
	for index := range requested {
		inputs[index] = requested[index].input
	}
	verified, err := renderer.verifier.VerifyCitations(ctx, snapshot.WorkspaceID, inputs)
	if err != nil {
		return nil, false, err
	}
	if len(verified) != len(requested) {
		return nil, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_EVIDENCE_RESULT_INVALID", false, "artifact verifier returned an incomplete evidence batch")
	}
	byIdentity := make(map[string]artifactdomain.Citation, len(verified))
	for _, citation := range verified {
		key := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
		if _, duplicate := byIdentity[key]; duplicate {
			return nil, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_EVIDENCE_RESULT_INVALID", false, "artifact verifier returned duplicate evidence")
		}
		byIdentity[key] = citation
	}
	result := make([]verifiedEvidence, len(requested))
	for index, item := range requested {
		key := string(item.frozen.SourceVersionID) + "\x00" + string(item.frozen.SourceSpanID)
		citation, found := byIdentity[key]
		if !found || !citation.Verified || citation.VerifiedContentHash != item.frozen.ContentHash || hashText(citation.Excerpt) != item.frozen.ExcerptHash {
			return nil, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_FROZEN_EVIDENCE_DRIFT", false, "reopened evidence differs from the immutable snapshot")
		}
		result[index] = verifiedEvidence{input: item.input, citation: citation}
	}
	return result, capacityLimited, nil
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func boundedUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "..."
}

func indentExcerpt(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\n  ")
}
