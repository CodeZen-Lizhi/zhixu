package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type SynthesisManuscriptRoot struct {
	GrantID        foundation.ID
	Fingerprint    string
	BindingVersion int64
}

// 根目录所属模块证明进程能力，并锁定其持久绑定。
type SynthesisManuscriptRootReader interface {
	ReadSynthesisManuscriptRootScoped(context.Context, foundation.TransactionScope, foundation.ID) (SynthesisManuscriptRoot, error)
}
