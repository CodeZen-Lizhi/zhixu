package domain

// SynthesisSourceReviewParagraph 标识一个不可变复核快照中精确的 UTF-8 出现位置；
// 它本身不宣称语义支持。
type SynthesisSourceReviewParagraph struct {
	Label     string `json:"label"`
	Ordinal   int    `json:"ordinal"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Hash      string `json:"hash"`
}
