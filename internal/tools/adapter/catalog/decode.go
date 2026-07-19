package catalog

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
)

const (
	maxQueryBytes      = 8 * 1024
	maxCitationIDBytes = 128
	maxStableRefBytes  = 256
	maxContentBytes    = 1024 * 1024
	maxDiffSideBytes   = 512 * 1024
	maxGitChanges      = 100000
)

var (
	stableTokenPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,63}$`)
	stableRefPattern   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}:[A-Za-z0-9][A-Za-z0-9._:-]{0,190}$`)
)

type searchKnowledgeInput struct {
	Query            string   `json:"query"`
	Mode             string   `json:"mode"`
	Limit            int      `json:"limit"`
	SourceIDs        []string `json:"source_ids"`
	SourceVersionIDs []string `json:"source_version_ids"`
}

type searchKnowledgeOutput struct {
	IndexVersionID string                `json:"index_version_id"`
	EffectiveMode  string                `json:"effective_mode"`
	Items          []searchKnowledgeItem `json:"items"`
	Degradations   []string              `json:"degradations"`
}

type searchKnowledgeItem struct {
	CitationID      string `json:"citation_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	Rank            int    `json:"rank"`
	Snippet         string `json:"snippet"`
}

type readSourceInput struct {
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
}

type readSourceOutput struct {
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	Excerpt         string `json:"excerpt"`
}

type readDocumentInput struct {
	DocumentID string `json:"document_id"`
}

type readDocumentOutput struct {
	DocumentID        string  `json:"document_id"`
	DocumentVersionID string  `json:"document_version_id"`
	ContentHash       string  `json:"content_hash"`
	Title             string  `json:"title"`
	Text              *string `json:"text"`
}

type fetchWebPageInput struct {
	URL string `json:"url"`
}

type fetchWebPageOutput struct {
	FinalURL      string  `json:"final_url"`
	FetchedAt     string  `json:"fetched_at"`
	ContentType   string  `json:"content_type"`
	ByteCount     *int64  `json:"byte_count"`
	ContentHash   string  `json:"content_hash"`
	Text          *string `json:"text"`
	UntrustedData *bool   `json:"untrusted_data"`
}

type citationTuple struct {
	CitationID      string `json:"citation_id"`
	IndexVersionID  string `json:"index_version_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
}

type validateCitationInput struct {
	Citations []citationTuple `json:"citations"`
}

type citationValidationResult struct {
	citationTuple
	Valid      *bool  `json:"valid"`
	ReasonCode string `json:"reason_code"`
}

type validateCitationOutput struct {
	Results []citationValidationResult `json:"results"`
}

type calculateDiffInput struct {
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type calculateDiffOutput struct {
	Changed    *bool   `json:"changed"`
	BeforeHash string  `json:"before_hash"`
	AfterHash  string  `json:"after_hash"`
	Patch      *string `json:"patch"`
}

type readGitStatusInput struct{}

type readGitStatusOutput struct {
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	ObjectFormat   string `json:"object_format"`
	Clean          *bool  `json:"clean"`
	StagedCount    *int   `json:"staged_count"`
	UnstagedCount  *int   `json:"unstaged_count"`
	UntrackedCount *int   `json:"untracked_count"`
	ConflictCount  *int   `json:"conflict_count"`
}

type writebackExecutionInput struct {
	WritebackExecutionID string `json:"writeback_execution_id"`
}

type applyApprovedPatchOutput struct {
	WritebackExecutionID string `json:"writeback_execution_id"`
	ResultRef            string `json:"result_ref"`
	ResultHash           string `json:"result_hash"`
	Status               string `json:"status"`
}

type createGitCommitOutput struct {
	WritebackExecutionID string `json:"writeback_execution_id"`
	ResultRef            string `json:"result_ref"`
	CommitOID            string `json:"commit_oid"`
	Status               string `json:"status"`
}

type rebuildIndexInput struct {
	ReindexDeliveryID string `json:"reindex_delivery_id"`
}

type rebuildIndexOutput struct {
	ReindexDeliveryID string `json:"reindex_delivery_id"`
	IndexVersionID    string `json:"index_version_id"`
	ResultRef         string `json:"result_ref"`
	Status            string `json:"status"`
}

type runRegressionEvaluationInput struct {
	DatasetID      string `json:"dataset_id"`
	IndexVersionID string `json:"index_version_id"`
}

type runRegressionEvaluationOutput struct {
	EvaluationRunID string `json:"evaluation_run_id"`
	ResultRef       string `json:"result_ref"`
	Passed          *bool  `json:"passed"`
	Status          string `json:"status"`
}

var (
	decodeSearchKnowledgeInput          = strictDecoder(maxSmallDocumentBytes, validateSearchKnowledgeInput)
	decodeSearchKnowledgeOutput         = strictDecoder(maxContentDocumentBytes, validateSearchKnowledgeOutput)
	decodeReadSourceInput               = strictDecoder(maxSmallDocumentBytes, validateReadSourceInput)
	decodeReadSourceOutput              = strictDecoder(maxMediumDocumentBytes, validateReadSourceOutput)
	decodeReadDocumentInput             = strictDecoder(maxSmallDocumentBytes, validateReadDocumentInput)
	decodeReadDocumentOutput            = strictDecoder(maxContentDocumentBytes, validateReadDocumentOutput)
	decodeFetchWebPageInput             = strictDecoder(maxSmallDocumentBytes, validateFetchWebPageInput)
	decodeFetchWebPageOutput            = strictDecoder(maxContentDocumentBytes, validateFetchWebPageOutput)
	decodeValidateCitationInput         = strictDecoder(maxMediumDocumentBytes, validateValidateCitationInput)
	decodeValidateCitationOutput        = strictDecoder(maxMediumDocumentBytes, validateValidateCitationOutput)
	decodeCalculateDiffInput            = strictDecoder(maxContentDocumentBytes, validateCalculateDiffInput)
	decodeCalculateDiffOutput           = strictDecoder(maxContentDocumentBytes, validateCalculateDiffOutput)
	decodeReadGitStatusInput            = strictDecoder(maxSmallDocumentBytes, func(readGitStatusInput) error { return nil })
	decodeReadGitStatusOutput           = strictDecoder(maxSmallDocumentBytes, validateReadGitStatusOutput)
	decodeApplyApprovedPatchInput       = strictDecoder(maxSmallDocumentBytes, validateWritebackExecutionInput)
	decodeApplyApprovedPatchOutput      = strictDecoder(maxSmallDocumentBytes, validateApplyApprovedPatchOutput)
	decodeCreateGitCommitInput          = strictDecoder(maxSmallDocumentBytes, validateWritebackExecutionInput)
	decodeCreateGitCommitOutput         = strictDecoder(maxSmallDocumentBytes, validateCreateGitCommitOutput)
	decodeRebuildIndexInput             = strictDecoder(maxSmallDocumentBytes, validateRebuildIndexInput)
	decodeRebuildIndexOutput            = strictDecoder(maxSmallDocumentBytes, validateRebuildIndexOutput)
	decodeRunRegressionEvaluationInput  = strictDecoder(maxSmallDocumentBytes, validateRunRegressionEvaluationInput)
	decodeRunRegressionEvaluationOutput = strictDecoder(maxSmallDocumentBytes, validateRunRegressionEvaluationOutput)
)

func strictDecoder[T any](maxDocumentBytes int64, validate func(T) error) application.DocumentDecoder {
	return func(raw []byte) (json.RawMessage, error) {
		limits := strictjson.DefaultLimits()
		limits.MaxDocumentBytes = int(maxDocumentBytes)
		limits.MaxDepth = 8
		limits.MaxStringBytes = int(maxDocumentBytes)
		limits.MaxArrayItems = 500
		limits.MaxObjectFields = 128
		value, err := strictjson.DecodeObject(raw, limits, validate)
		if err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return canonical, nil
	}
}

func validateSearchKnowledgeInput(value searchKnowledgeInput) error {
	if !validText(value.Query, 1, maxQueryBytes) || !validSearchMode(value.Mode) || value.Limit < 1 || value.Limit > 100 ||
		value.SourceIDs == nil || value.SourceVersionIDs == nil || len(value.SourceIDs) > 100 || len(value.SourceVersionIDs) > 100 ||
		!validUniqueIDs(value.SourceIDs) || !validUniqueIDs(value.SourceVersionIDs) {
		return invalidDocument()
	}
	return nil
}

func validateSearchKnowledgeOutput(value searchKnowledgeOutput) error {
	if !validID(value.IndexVersionID) || !validSearchMode(value.EffectiveMode) || value.Items == nil || len(value.Items) > 100 ||
		value.Degradations == nil || len(value.Degradations) > 16 || !validUniqueTokens(value.Degradations) {
		return invalidDocument()
	}
	seen := make(map[string]struct{}, len(value.Items))
	for _, item := range value.Items {
		if !validText(item.CitationID, 1, maxCitationIDBytes) || !validID(item.ChunkID) || !validID(item.SourceVersionID) ||
			!validID(item.SourceSpanID) || !validHash(item.ContentHash, 64) || item.Rank < 1 || item.Rank > 100 ||
			!validText(item.Snippet, 1, 4*1024) {
			return invalidDocument()
		}
		if _, duplicate := seen[item.CitationID]; duplicate {
			return invalidDocument()
		}
		seen[item.CitationID] = struct{}{}
	}
	return nil
}

func validateReadSourceInput(value readSourceInput) error {
	if !validID(value.SourceVersionID) || !validID(value.SourceSpanID) {
		return invalidDocument()
	}
	return nil
}

func validateReadSourceOutput(value readSourceOutput) error {
	if validateReadSourceInput(readSourceInput{SourceVersionID: value.SourceVersionID, SourceSpanID: value.SourceSpanID}) != nil ||
		!validHash(value.ContentHash, 64) || !validText(value.Excerpt, 1, int(maxMediumDocumentBytes)) {
		return invalidDocument()
	}
	return nil
}

func validateReadDocumentInput(value readDocumentInput) error {
	if !validID(value.DocumentID) {
		return invalidDocument()
	}
	return nil
}

func validateReadDocumentOutput(value readDocumentOutput) error {
	if !validID(value.DocumentID) || !validID(value.DocumentVersionID) || !validHash(value.ContentHash, 64) ||
		!validText(value.Title, 1, 1024) || value.Text == nil || !validText(*value.Text, 0, maxContentBytes) {
		return invalidDocument()
	}
	return nil
}

func validateFetchWebPageInput(value fetchWebPageInput) error {
	if !validPublicURLShape(value.URL) {
		return invalidDocument()
	}
	return nil
}

func validateFetchWebPageOutput(value fetchWebPageOutput) error {
	fetchedAt, err := time.Parse(time.RFC3339Nano, value.FetchedAt)
	if !validPublicURLShape(value.FinalURL) || err != nil || fetchedAt.IsZero() ||
		(value.ContentType != "text/plain" && value.ContentType != "text/html") || value.ByteCount == nil || *value.ByteCount < 0 || *value.ByteCount > maxContentBytes ||
		!validHash(value.ContentHash, 64) || value.Text == nil || !validText(*value.Text, 0, maxContentBytes) || value.UntrustedData == nil || !*value.UntrustedData {
		return invalidDocument()
	}
	return nil
}

func validateValidateCitationInput(value validateCitationInput) error {
	if len(value.Citations) < 1 || len(value.Citations) > 500 || !validUniqueCitationTuples(value.Citations) {
		return invalidDocument()
	}
	return nil
}

func validateValidateCitationOutput(value validateCitationOutput) error {
	if len(value.Results) < 1 || len(value.Results) > 500 {
		return invalidDocument()
	}
	seen := make(map[string]struct{}, len(value.Results))
	for _, result := range value.Results {
		if !validCitationTuple(result.citationTuple) || result.Valid == nil || !validCitationReason(result.ReasonCode) ||
			(*result.Valid && result.ReasonCode != "OK") || (!*result.Valid && result.ReasonCode == "OK") {
			return invalidDocument()
		}
		key := citationKey(result.citationTuple)
		if _, duplicate := seen[key]; duplicate {
			return invalidDocument()
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCalculateDiffInput(value calculateDiffInput) error {
	if value.Before == nil || value.After == nil || !validText(*value.Before, 0, maxDiffSideBytes) || !validText(*value.After, 0, maxDiffSideBytes) {
		return invalidDocument()
	}
	return nil
}

func validateCalculateDiffOutput(value calculateDiffOutput) error {
	if value.Changed == nil || !validHash(value.BeforeHash, 64) || !validHash(value.AfterHash, 64) || value.Patch == nil ||
		!validText(*value.Patch, 0, maxContentBytes) || (*value.Changed != (value.BeforeHash != value.AfterHash)) || (!*value.Changed && *value.Patch != "") {
		return invalidDocument()
	}
	return nil
}

func validateReadGitStatusOutput(value readGitStatusOutput) error {
	if !validText(value.Branch, 1, 255) || !validGitOID(value.Head) ||
		(value.ObjectFormat != "sha1" && value.ObjectFormat != "sha256") ||
		(value.ObjectFormat == "sha1" && len(value.Head) != 40) || (value.ObjectFormat == "sha256" && len(value.Head) != 64) ||
		value.Clean == nil || !validCount(value.StagedCount) || !validCount(value.UnstagedCount) || !validCount(value.UntrackedCount) || !validCount(value.ConflictCount) {
		return invalidDocument()
	}
	changes := *value.StagedCount + *value.UnstagedCount + *value.UntrackedCount + *value.ConflictCount
	if *value.Clean != (changes == 0) {
		return invalidDocument()
	}
	return nil
}

func validateWritebackExecutionInput(value writebackExecutionInput) error {
	if !validID(value.WritebackExecutionID) {
		return invalidDocument()
	}
	return nil
}

func validateApplyApprovedPatchOutput(value applyApprovedPatchOutput) error {
	if !validID(value.WritebackExecutionID) || !validStableRef(value.ResultRef) || !validHash(value.ResultHash, 64) || value.Status != "APPLIED" {
		return invalidDocument()
	}
	return nil
}

func validateCreateGitCommitOutput(value createGitCommitOutput) error {
	if !validID(value.WritebackExecutionID) || !validStableRef(value.ResultRef) || !validGitOID(value.CommitOID) || value.Status != "COMMITTED" {
		return invalidDocument()
	}
	return nil
}

func validateRebuildIndexInput(value rebuildIndexInput) error {
	if !validID(value.ReindexDeliveryID) {
		return invalidDocument()
	}
	return nil
}

func validateRebuildIndexOutput(value rebuildIndexOutput) error {
	if !validID(value.ReindexDeliveryID) || !validID(value.IndexVersionID) || !validStableRef(value.ResultRef) || value.Status != "SUCCEEDED" {
		return invalidDocument()
	}
	return nil
}

func validateRunRegressionEvaluationInput(value runRegressionEvaluationInput) error {
	if !validID(value.DatasetID) || !validID(value.IndexVersionID) {
		return invalidDocument()
	}
	return nil
}

func validateRunRegressionEvaluationOutput(value runRegressionEvaluationOutput) error {
	if !validID(value.EvaluationRunID) || !validStableRef(value.ResultRef) || value.Passed == nil ||
		(value.Status != "PASSED" && value.Status != "FAILED") || (*value.Passed != (value.Status == "PASSED")) {
		return invalidDocument()
	}
	return nil
}

func validSearchMode(value string) bool {
	return value == "keyword" || value == "semantic" || value == "hybrid"
}

func validPublicURLShape(value string) bool {
	if !validText(value, 1, 8192) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Opaque == "" && parsed.User == nil && parsed.Hostname() != "" && parsed.Fragment == "" && len(parsed.Hostname()) <= 253 && validURLPort(parsed.Port())
}

func validURLPort(value string) bool {
	if value == "" {
		return true
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func validID(value string) bool {
	parsed, err := foundation.ParseID(value)
	return err == nil && string(parsed) == value
}

func validUniqueIDs(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validID(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validUniqueTokens(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !stableTokenPattern.MatchString(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validUniqueCitationTuples(values []citationTuple) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validCitationTuple(value) {
			return false
		}
		key := citationKey(value)
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validCitationTuple(value citationTuple) bool {
	return validText(value.CitationID, 1, maxCitationIDBytes) && validID(value.IndexVersionID) && validID(value.ChunkID) &&
		validID(value.SourceVersionID) && validID(value.SourceSpanID)
}

func citationKey(value citationTuple) string {
	return strings.Join([]string{value.CitationID, value.IndexVersionID, value.ChunkID, value.SourceVersionID, value.SourceSpanID}, "\x00")
}

func validCitationReason(value string) bool {
	switch value {
	case "OK", "CITATION_UNRESOLVABLE", "EVIDENCE_INELIGIBLE", "BINDING_MISMATCH":
		return true
	default:
		return false
	}
}

func validCount(value *int) bool {
	return value != nil && *value >= 0 && *value <= maxGitChanges
}

func validHash(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validGitOID(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validHash(value, len(value))
}

func validStableRef(value string) bool {
	return len(value) <= maxStableRefBytes && stableRefPattern.MatchString(value)
}

func validText(value string, minBytes, maxBytes int) bool {
	return len(value) >= minBytes && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') &&
		(minBytes == 0 || strings.TrimSpace(value) != "")
}

func invalidDocument() error {
	return errors.New("tool document does not satisfy the versioned contract")
}
