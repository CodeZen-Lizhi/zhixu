package postgres

import (
	"strconv"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestGroupNodeRefsKeepsLockQueriesBoundedForMaximumConflict(t *testing.T) {
	refs := make([]domain.NodeRef, 0, domain.MaxBatchLimit+3)
	for index := 0; index < domain.MaxBatchLimit; index++ {
		refs = append(refs, domain.NodeRef{Type: domain.NodeTypeClaim, ID: foundation.ID("claim-" + strconv.Itoa(index))})
	}
	refs = append(refs,
		domain.NodeRef{Type: domain.NodeTypeTopic, ID: foundation.ID("topic-1")},
		refs[0],
		domain.NodeRef{Type: domain.NodeTypeTopic, ID: foundation.ID("topic-1")},
	)

	groups, err := groupNodeRefs(refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Type != domain.NodeTypeClaim || len(groups[0].IDs) != domain.MaxBatchLimit ||
		groups[1].Type != domain.NodeTypeTopic || len(groups[1].IDs) != 1 {
		t.Fatalf("groups=%#v", groups)
	}
}

func TestGroupNodeRefsRejectsUnsupportedNodeType(t *testing.T) {
	if _, err := groupNodeRefs([]domain.NodeRef{{Type: domain.NodeType("FUTURE"), ID: foundation.ID("node-1")}}); err == nil {
		t.Fatal("unsupported node type must fail closed")
	}
}
