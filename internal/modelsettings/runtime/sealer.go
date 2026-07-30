package runtime

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

type unavailableSecretSealer struct{ cause error }

// NewUnavailableSecretSealer keeps non-secret Settings reads available when the key cannot be loaded.
func NewUnavailableSecretSealer(cause error) modelsettingsapplication.SecretSealer {
	if cause == nil {
		cause = errors.New("model settings secret key is unavailable")
	}
	return unavailableSecretSealer{cause: cause}
}

func (sealer unavailableSecretSealer) Seal(modelsettingsdomain.Secret, modelsettingsdomain.SecretContext) (modelsettingsdomain.EncryptedSecret, error) {
	return modelsettingsdomain.EncryptedSecret{}, sealer.err()
}

func (sealer unavailableSecretSealer) Open(modelsettingsdomain.EncryptedSecret, modelsettingsdomain.SecretContext) (modelsettingsdomain.Secret, error) {
	return modelsettingsdomain.Secret{}, sealer.err()
}

func (sealer unavailableSecretSealer) err() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeSecretUnavailable, false, sealer.cause)
}
