package domain

import foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"

// DecodeLimits 限制单次结构化输出解析可消耗的资源。
type DecodeLimits = foundationstrictjson.Limits

// DefaultDecodeLimits 返回 Agent 任务 Schema 的默认安全边界。
func DefaultDecodeLimits() DecodeLimits {
	return foundationstrictjson.DefaultLimits()
}

// DecodeStrict 解析单个 JSON object，拒绝重复键、未知字段、尾随值和资源越界。
func DecodeStrict[T any](raw []byte, limits DecodeLimits, validate func(T) error) (T, error) {
	var zero T
	decoded, err := foundationstrictjson.DecodeObject(raw, limits, validate)
	if err == nil {
		return decoded, nil
	}
	kind, ok := foundationstrictjson.KindOf(err)
	if !ok {
		return zero, err
	}
	switch kind {
	case foundationstrictjson.ErrorKindLimitExceeded:
		return zero, invalid(ErrorCodeStructuredOutputLimitExceeded, err.Error())
	default:
		return zero, invalid(ErrorCodeStructuredOutputInvalid, err.Error())
	}
}
