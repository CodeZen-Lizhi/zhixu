package application

import (
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func workspaceAnalysisAllowedEvidenceRefsForVersion(refs []string, version int64) bool {
	if version == 1 {
		return workspaceAnalysisAllowedEvidenceRefs(refs)
	}
	if version != 2 || len(refs) < 1 || len(refs) > domain.WorkspaceAnalysisV2MaxSourceReads {
		return false
	}
	previous := 0
	for _, ref := range refs {
		if !domain.ValidWorkspaceAnalysisV2EvidenceRef(ref) {
			return false
		}
		ordinal, _ := strconv.Atoi(ref[1:])
		if ordinal <= previous {
			return false
		}
		previous = ordinal
	}
	return true
}

func workspaceAnalysisCandidateRefsAllowedForVersion(candidateRefs, allowedRefs []string, version int64) bool {
	if version == 1 {
		return workspaceAnalysisCandidateRefsAllowed(candidateRefs, allowedRefs)
	}
	if !workspaceAnalysisAllowedEvidenceRefsForVersion(allowedRefs, version) || len(candidateRefs) < 1 || len(candidateRefs) > len(allowedRefs) {
		return false
	}
	allowed := make(map[string]bool, len(allowedRefs))
	for _, ref := range allowedRefs {
		allowed[ref] = true
	}
	for _, ref := range candidateRefs {
		if !allowed[ref] {
			return false
		}
		allowed[ref] = false
	}
	return true
}
