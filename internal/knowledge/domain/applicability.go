package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ApplicabilitySchemaV1 是当前唯一支持的适用条件规范版本。
	ApplicabilitySchemaV1 = "knowledge-applicability/v1"
	// MaxApplicabilityBytes 是输入及 canonical JSON 的最大字节数。
	MaxApplicabilityBytes = 16 * 1024
	// MaxApplicabilityDepth 是 object/array 的最大嵌套深度。
	MaxApplicabilityDepth = 16
)

// Applicability 是版本化、可稳定比较的 Claim/Relation 适用条件。
type Applicability struct {
	SchemaVersion string
	CanonicalJSON json.RawMessage
	Hash          string
}

// ParseApplicability 严格解析 JSON object，拒绝重复键并生成稳定 canonical JSON 和 SHA-256。
func ParseApplicability(raw json.RawMessage) (Applicability, error) {
	if len(raw) == 0 || len(raw) > MaxApplicabilityBytes || !utf8.Valid(raw) {
		return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, "applicability must be valid bounded utf-8 json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCanonicalJSON(decoder, 1)
	if err != nil {
		return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, err.Error())
	}
	if _, ok := value.(map[string]any); !ok {
		return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, "applicability root must be an object")
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, fmt.Sprintf("unexpected trailing token %v", token))
		}
		return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, "applicability contains trailing or invalid json")
	}
	canonical, err := encodeCanonicalJSON(nil, value)
	if err != nil || len(canonical) > MaxApplicabilityBytes {
		return Applicability{}, invalid(ErrorCodeApplicabilityInvalid, "canonical applicability exceeds byte limit")
	}
	hash := applicabilityHash(canonical)
	return Applicability{SchemaVersion: ApplicabilitySchemaV1, CanonicalJSON: canonical, Hash: hash}, nil
}

// ValidateApplicability 验证持久值仍是 canonical v1 JSON 且哈希没有被篡改。
func ValidateApplicability(value Applicability) error {
	if value.SchemaVersion != ApplicabilitySchemaV1 || len(value.CanonicalJSON) == 0 || len(value.CanonicalJSON) > MaxApplicabilityBytes {
		return invalid(ErrorCodeApplicabilityInvalid, "applicability version or payload is invalid")
	}
	parsed, err := ParseApplicability(value.CanonicalJSON)
	if err != nil || !bytes.Equal(parsed.CanonicalJSON, value.CanonicalJSON) || parsed.Hash != value.Hash {
		return inconsistent(ErrorCodeApplicabilityInvalid, "applicability canonical payload or hash is inconsistent")
	}
	return nil
}

func decodeCanonicalJSON(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		if depth > MaxApplicabilityDepth {
			return nil, errorsNew("applicability nesting is too deep")
		}
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return nil, keyErr
				}
				key, ok := keyToken.(string)
				if !ok || !utf8.ValidString(key) {
					return nil, errorsNew("applicability object key is invalid")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, errorsNew("applicability contains duplicate object key")
				}
				child, childErr := decodeCanonicalJSON(decoder, depth+1)
				if childErr != nil {
					return nil, childErr
				}
				object[key] = child
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim('}') {
				return nil, errorsNew("applicability object is not terminated")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, childErr := decodeCanonicalJSON(decoder, depth+1)
				if childErr != nil {
					return nil, childErr
				}
				array = append(array, child)
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim(']') {
				return nil, errorsNew("applicability array is not terminated")
			}
			return array, nil
		default:
			return nil, errorsNew("unexpected applicability delimiter")
		}
	case json.Number:
		canonical, numberErr := canonicalJSONNumber(string(value))
		if numberErr != nil {
			return nil, numberErr
		}
		return canonicalNumber(canonical), nil
	case string:
		if !utf8.ValidString(value) {
			return nil, errorsNew("applicability string is invalid utf-8")
		}
		return value, nil
	case bool, nil:
		return value, nil
	default:
		return nil, errorsNew("unsupported applicability value")
	}
}

type canonicalNumber string

func encodeCanonicalJSON(dst []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dst = append(dst, '{')
		for index, key := range keys {
			if index > 0 {
				dst = append(dst, ',')
			}
			encodedKey, _ := json.Marshal(key)
			dst = append(dst, encodedKey...)
			dst = append(dst, ':')
			var err error
			dst, err = encodeCanonicalJSON(dst, typed[key])
			if err != nil {
				return nil, err
			}
		}
		return append(dst, '}'), nil
	case []any:
		dst = append(dst, '[')
		for index, item := range typed {
			if index > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = encodeCanonicalJSON(dst, item)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case canonicalNumber:
		return append(dst, string(typed)...), nil
	case string:
		encoded, _ := json.Marshal(typed)
		return append(dst, encoded...), nil
	case bool:
		return strconv.AppendBool(dst, typed), nil
	case nil:
		return append(dst, "null"...), nil
	default:
		return nil, errorsNew("unsupported canonical json value")
	}
}

func canonicalJSONNumber(value string) (string, error) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	mantissa := value
	exponent := 0
	if position := strings.IndexAny(value, "eE"); position >= 0 {
		mantissa = value[:position]
		parsed, err := strconv.ParseInt(value[position+1:], 10, 32)
		if err != nil {
			return "", errorsNew("applicability number exponent is invalid")
		}
		exponent = int(parsed)
	}
	dot := strings.IndexByte(mantissa, '.')
	fractionDigits := 0
	if dot >= 0 {
		fractionDigits = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return "0", nil
	}
	decimalPosition := len(mantissa) - fractionDigits + exponent
	leadingZeros := len(mantissa) - len(digits)
	decimalPosition -= leadingZeros
	var result string
	switch {
	case decimalPosition <= 0:
		if 2-decimalPosition+len(digits) > MaxApplicabilityBytes {
			return "", errorsNew("applicability number expansion is too large")
		}
		result = "0." + strings.Repeat("0", -decimalPosition) + digits
	case decimalPosition >= len(digits):
		if decimalPosition > MaxApplicabilityBytes {
			return "", errorsNew("applicability number expansion is too large")
		}
		result = digits + strings.Repeat("0", decimalPosition-len(digits))
	default:
		result = digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if strings.ContainsRune(result, '.') {
		result = strings.TrimRight(result, "0")
		result = strings.TrimRight(result, ".")
	}
	if negative && result != "0" {
		result = "-" + result
	}
	return result, nil
}

func applicabilityHash(canonical []byte) string {
	digest := sha256.Sum256(append([]byte(ApplicabilitySchemaV1+"\n"), canonical...))
	return hex.EncodeToString(digest[:])
}

type stringError string

func (e stringError) Error() string  { return string(e) }
func errorsNew(message string) error { return stringError(message) }
