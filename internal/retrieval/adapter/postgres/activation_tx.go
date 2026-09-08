package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

type activationLockedIndexes struct {
	target  domain.IndexVersion
	current *domain.IndexVersion
}
