package application

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"
)

// SynthesisProcessingPage is the runtime owner's bounded source-processing view.
type SynthesisProcessingPage struct {
	Items    []SynthesisProcessing
	NextTime *time.Time
	NextID   foundation.ID
}
