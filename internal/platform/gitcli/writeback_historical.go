package gitcli

import (
	"context"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
)

func historicalSameByteLookup(ctx context.Context, l changecontrol.GitCommitLookup) bool {
	a, ok := changecontrol.HistoricalRepublishGitAuthorityFromContext(ctx)
	return ok && a.MatchesLookup(l) && l.DiffHash == sha256Hex(nil)
}
func historicalSameByteTarget(ctx context.Context, wPath, head, result string) bool {
	a, ok := changecontrol.HistoricalRepublishGitAuthorityFromContext(ctx)
	return ok && a.TargetPath == wPath && a.ApprovedGitHead == head && a.ContentHash == result
}
func fixedAuthorizedCommitMessage(ctx context.Context, l changecontrol.GitCommitLookup) string {
	message := fixedCommitMessage(l)
	if historicalSameByteLookup(ctx, l) {
		a, _ := changecontrol.HistoricalRepublishGitAuthorityFromContext(ctx)
		message += "Zhixu-Historical-Republish-Receipt: " + string(a.ReceiptID) + "\n"
	}
	return message
}

// 历史授权的空增量保持整棵树和精确目标
// blob 不变。这不会放宽普通文件写回的限制。
func sameHistoricalBlob(ctx context.Context, l changecontrol.GitCommitLookup) bool {
	return historicalSameByteLookup(ctx, l) && strings.EqualFold(l.BaseBlobID, l.ResultBlobID)
}
