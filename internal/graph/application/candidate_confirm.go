package application

import "github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"

// SemanticLinkCandidateConfirmCommand 是 Candidate Application 交给跨模块
// PostgreSQL UoW 的确认命令。
type SemanticLinkCandidateConfirmCommand = candidateconfirm.Command

// SemanticLinkCandidateConfirmResult 是 Candidate、Proposal 与 Decision 的
// 单事务持久化结果。
type SemanticLinkCandidateConfirmResult = candidateconfirm.Result

// SemanticLinkCandidateConfirmPort 是 Candidate Confirm 的唯一写入端口。
type SemanticLinkCandidateConfirmPort = candidateconfirm.Port
