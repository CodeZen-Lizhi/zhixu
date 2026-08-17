package gitcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	statusAggregateOutputLimit   = 1 << 20
	statusMetadataOutputLimit    = 16 << 10
	statusAggregateCountLimit    = 100_000
	statusAggregateBranchLimit   = 255
	statusAggregateUpstreamLimit = 1024
)

const (
	// StatusObjectFormatSHA1 表示仓库使用 SHA-1 Git 对象 ID。
	StatusObjectFormatSHA1 = "sha1"
	// StatusObjectFormatSHA256 表示仓库使用 SHA-256 Git 对象 ID。
	StatusObjectFormatSHA256 = "sha256"
)

var (
	errStatusAggregateInvalid       = errors.New("git status aggregate is invalid")
	errStatusAggregateAmbiguous     = errors.New("git status aggregate is ambiguous")
	errStatusAggregateEncoding      = errors.New("git status aggregate encoding is invalid")
	errStatusAggregateUnsupported   = errors.New("git status aggregate record is unsupported")
	errStatusAggregateSubmodule     = errors.New("git status aggregate contains a submodule")
	errStatusAggregateDetached      = errors.New("git status aggregate has a detached head")
	errStatusAggregateUnborn        = errors.New("git status aggregate has an unborn head")
	errStatusAggregateCountOverflow = errors.New("git status aggregate count exceeds limit")
)

// StatusAggregate 是 Workspace 绑定的只读 Git 聚合；它不保存仓库根、文件路径、Diff 或正文。
type StatusAggregate struct {
	WorkspaceID    foundation.ID `json:"-"`
	Branch         string
	Head           string
	ObjectFormat   string
	Clean          bool
	StagedCount    int
	UnstagedCount  int
	UntrackedCount int
	ConflictCount  int
}

// StatusAggregateClient 只执行固定的 Git 状态读取，不拥有 Approval Snapshot 或任何写能力。
type StatusAggregateClient struct {
	git        Client
	workspaces WorkspaceRepository
}

// NewStatusAggregateClient 创建通过服务端 Workspace ID 解析精确仓库根的只读状态客户端。
func NewStatusAggregateClient(git Client, workspaces WorkspaceRepository) (*StatusAggregateClient, error) {
	if nilStatusWorkspaceRepository(workspaces) {
		return nil, statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_WORKSPACE_REPOSITORY_UNAVAILABLE", false, errors.New("workspace repository is required"))
	}
	if strings.TrimSpace(git.executable) == "" {
		git = New("")
	}
	return &StatusAggregateClient{git: git, workspaces: workspaces}, nil
}

// Inspect 返回 attached HEAD 与 porcelain-v2 脏状态计数；所有 Git argv 和环境均由客户端固定。
func (c *StatusAggregateClient) Inspect(ctx context.Context, workspaceID foundation.ID) (StatusAggregate, error) {
	if c == nil || nilStatusWorkspaceRepository(c.workspaces) {
		return StatusAggregate{}, statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_INSPECTOR_UNAVAILABLE", false, errors.New("git status inspector is unavailable"))
	}
	if ctx == nil || !validStatusWorkspaceID(workspaceID) {
		return StatusAggregate{}, statusAggregateError(foundation.ErrorInvalidInput, "GIT_STATUS_INPUT_INVALID", false, errors.New("git status input is invalid"))
	}
	if err := ctx.Err(); err != nil {
		return StatusAggregate{}, classifyStatusAggregateCommand("GIT_STATUS_WORKSPACE_UNAVAILABLE", err)
	}

	root, err := c.resolveStatusRoot(ctx, workspaceID)
	if err != nil {
		return StatusAggregate{}, err
	}
	configDir, err := os.MkdirTemp("", "zhixu-git-status-config-")
	if err != nil {
		return StatusAggregate{}, statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_CONFIG_ISOLATION_UNAVAILABLE", false, err)
	}
	defer func() { _ = os.RemoveAll(configDir) }()

	topLevel, err := c.git.runCommand(ctx, root, commandOptions{
		ReadOnly: true, MaxOutputBytes: statusMetadataOutputLimit, IsolatedConfigDir: configDir,
	}, "rev-parse", "--show-toplevel")
	if err != nil {
		if isMissingRepository(err) {
			return StatusAggregate{}, statusAggregateError(foundation.ErrorNotFound, "GIT_REPOSITORY_NOT_FOUND", false, errors.New("workspace is not a git repository"))
		}
		return StatusAggregate{}, classifyStatusAggregateCommand("GIT_STATUS_REPOSITORY_INSPECT_FAILED", err)
	}
	actualRoot, err := parseStatusPathLine(topLevel.Stdout)
	if err != nil {
		return StatusAggregate{}, classifyStatusAggregateParse(err)
	}
	if actualRoot != root {
		return StatusAggregate{}, statusAggregateError(foundation.ErrorPermissionDenied, "GIT_REPOSITORY_ROOT_MISMATCH", false, errors.New("workspace root does not match git repository root"))
	}

	formatResult, err := c.git.runCommand(ctx, root, commandOptions{
		ReadOnly: true, MaxOutputBytes: statusMetadataOutputLimit, IsolatedConfigDir: configDir,
	}, "rev-parse", "--show-object-format")
	if err != nil {
		return StatusAggregate{}, classifyStatusAggregateCommand("GIT_STATUS_OBJECT_FORMAT_FAILED", err)
	}
	objectFormat, err := parseStatusObjectFormat(formatResult.Stdout)
	if err != nil {
		return StatusAggregate{}, classifyStatusAggregateParse(err)
	}

	statusResult, err := c.git.runCommand(ctx, root, commandOptions{
		ReadOnly: true, MaxOutputBytes: statusAggregateOutputLimit, IsolatedConfigDir: configDir,
	}, "status", "--porcelain=v2", "--branch", "--no-ahead-behind", "-z", "--untracked-files=normal", "--ignore-submodules=all")
	if err != nil {
		return StatusAggregate{}, classifyStatusAggregateCommand("GIT_STATUS_INSPECT_FAILED", err)
	}
	aggregate, err := parseStatusAggregate(statusResult.Stdout, objectFormat)
	if err != nil {
		return StatusAggregate{}, classifyStatusAggregateParse(err)
	}
	if err := ctx.Err(); err != nil {
		return StatusAggregate{}, classifyStatusAggregateCommand("GIT_STATUS_INSPECT_FAILED", err)
	}
	aggregate.WorkspaceID = workspaceID
	return aggregate, nil
}

func nilStatusWorkspaceRepository(repository WorkspaceRepository) bool {
	if repository == nil {
		return true
	}
	value := reflect.ValueOf(repository)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (c *StatusAggregateClient) resolveStatusRoot(ctx context.Context, workspaceID foundation.ID) (string, error) {
	workspace, err := c.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", classifyStatusAggregateCommand("GIT_STATUS_WORKSPACE_UNAVAILABLE", ctxErr)
		}
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return "", err
		}
		return "", statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_WORKSPACE_UNAVAILABLE", false, err)
	}
	if workspace.ID != workspaceID {
		return "", statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_WORKSPACE_BINDING_INVALID", false, errors.New("workspace identity does not match request"))
	}
	if workspace.Status != workspacedomain.WorkspaceStatusActive ||
		workspace.Availability != workspacedomain.WorkspaceAvailabilityAvailable || !workspace.RemovedAt.IsZero() {
		return "", statusAggregateError(foundation.ErrorPermissionDenied, "GIT_STATUS_WORKSPACE_UNAVAILABLE", false, errors.New("workspace is not available for Git inspection"))
	}
	rawRoot := workspace.RootPath
	if rawRoot == "" || rawRoot != strings.TrimSpace(rawRoot) || !filepath.IsAbs(rawRoot) || strings.ContainsAny(rawRoot, "\x00\r\n") {
		return "", statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_WORKSPACE_ROOT_INVALID", false, errors.New("workspace root is invalid"))
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(rawRoot))
	if err != nil {
		return "", statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_STATUS_WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	return filepath.Clean(root), nil
}

func parseStatusPathLine(raw []byte) (string, error) {
	value, err := parseStatusSingleLine(raw)
	if err != nil || !filepath.IsAbs(value) {
		if err != nil {
			return "", err
		}
		return "", errStatusAggregateInvalid
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", errStatusAggregateInvalid
	}
	return filepath.Clean(canonical), nil
}

func parseStatusObjectFormat(raw []byte) (string, error) {
	value, err := parseStatusSingleLine(raw)
	if err != nil {
		return "", err
	}
	if value != StatusObjectFormatSHA1 && value != StatusObjectFormatSHA256 {
		return "", errStatusAggregateUnsupported
	}
	return value, nil
}

func parseStatusSingleLine(raw []byte) (string, error) {
	if len(raw) < 2 || len(raw) > statusMetadataOutputLimit || raw[len(raw)-1] != '\n' ||
		bytes.Count(raw, []byte{'\n'}) != 1 || bytes.IndexAny(raw, "\x00\r") >= 0 {
		return "", errStatusAggregateInvalid
	}
	value := raw[:len(raw)-1]
	if !utf8.Valid(value) {
		return "", errStatusAggregateEncoding
	}
	return string(value), nil
}

type statusNULReader struct {
	raw    []byte
	offset int
}

func newStatusNULReader(raw []byte) (*statusNULReader, error) {
	if len(raw) == 0 || len(raw) > statusAggregateOutputLimit || raw[len(raw)-1] != 0 {
		return nil, errStatusAggregateInvalid
	}
	return &statusNULReader{raw: raw}, nil
}

func (reader *statusNULReader) next() ([]byte, bool, error) {
	if reader.offset == len(reader.raw) {
		return nil, false, nil
	}
	end := bytes.IndexByte(reader.raw[reader.offset:], 0)
	if end < 0 {
		return nil, false, errStatusAggregateInvalid
	}
	value := reader.raw[reader.offset : reader.offset+end]
	reader.offset += end + 1
	if len(value) == 0 {
		return nil, false, errStatusAggregateInvalid
	}
	return value, true, nil
}

type statusBranchHeaders struct {
	oidSeen      bool
	headSeen     bool
	upstreamSeen bool
	aheadSeen    bool
	recordsSeen  bool
	oid          string
	head         string
}

func parseStatusAggregate(raw []byte, objectFormat string) (StatusAggregate, error) {
	if objectFormat != StatusObjectFormatSHA1 && objectFormat != StatusObjectFormatSHA256 {
		return StatusAggregate{}, errStatusAggregateUnsupported
	}
	reader, err := newStatusNULReader(raw)
	if err != nil {
		return StatusAggregate{}, err
	}
	headers := statusBranchHeaders{}
	aggregate := StatusAggregate{ObjectFormat: objectFormat}
	for {
		record, found, err := reader.next()
		if err != nil {
			return StatusAggregate{}, err
		}
		if !found {
			break
		}
		if bytes.HasPrefix(record, []byte("# ")) {
			if headers.recordsSeen {
				return StatusAggregate{}, errStatusAggregateAmbiguous
			}
			if err := parseStatusBranchHeader(&headers, record, objectFormat); err != nil {
				return StatusAggregate{}, err
			}
			continue
		}
		if !headers.oidSeen || !headers.headSeen || headers.upstreamSeen != headers.aheadSeen {
			return StatusAggregate{}, errStatusAggregateAmbiguous
		}
		headers.recordsSeen = true
		if err := classifyStatusRecord(reader, &aggregate, record, objectFormat); err != nil {
			return StatusAggregate{}, err
		}
	}
	if !headers.oidSeen || !headers.headSeen || headers.upstreamSeen != headers.aheadSeen {
		return StatusAggregate{}, errStatusAggregateAmbiguous
	}
	aggregate.Branch = headers.head
	aggregate.Head = headers.oid
	aggregate.Clean = aggregate.StagedCount == 0 && aggregate.UnstagedCount == 0 && aggregate.UntrackedCount == 0 && aggregate.ConflictCount == 0
	return aggregate, nil
}

func parseStatusBranchHeader(headers *statusBranchHeaders, record []byte, objectFormat string) error {
	key, value, found := bytes.Cut(record[2:], []byte{' '})
	if !found || len(value) == 0 {
		return errStatusAggregateInvalid
	}
	switch string(key) {
	case "branch.oid":
		if headers.oidSeen || headers.headSeen || headers.upstreamSeen || headers.aheadSeen {
			return errStatusAggregateAmbiguous
		}
		if bytes.Equal(value, []byte("(initial)")) {
			return errStatusAggregateUnborn
		}
		if !validStatusObjectID(value, objectFormat) {
			if !utf8.Valid(value) {
				return errStatusAggregateEncoding
			}
			return errStatusAggregateInvalid
		}
		headers.oidSeen = true
		headers.oid = string(value)
	case "branch.head":
		if !headers.oidSeen || headers.headSeen || headers.upstreamSeen || headers.aheadSeen {
			return errStatusAggregateAmbiguous
		}
		if bytes.Equal(value, []byte("(detached)")) {
			return errStatusAggregateDetached
		}
		if !validStatusReference(value, statusAggregateBranchLimit) {
			if !utf8.Valid(value) {
				return errStatusAggregateEncoding
			}
			return errStatusAggregateInvalid
		}
		headers.headSeen = true
		headers.head = string(value)
	case "branch.upstream":
		if !headers.oidSeen || !headers.headSeen || headers.upstreamSeen || headers.aheadSeen || !validStatusReference(value, statusAggregateUpstreamLimit) {
			if !utf8.Valid(value) {
				return errStatusAggregateEncoding
			}
			return errStatusAggregateAmbiguous
		}
		headers.upstreamSeen = true
	case "branch.ab":
		if !headers.upstreamSeen || headers.aheadSeen || !validStatusAheadBehind(value) {
			return errStatusAggregateAmbiguous
		}
		headers.aheadSeen = true
	default:
		return errStatusAggregateUnsupported
	}
	return nil
}

func classifyStatusRecord(reader *statusNULReader, aggregate *StatusAggregate, record []byte, objectFormat string) error {
	switch record[0] {
	case '1':
		fields := bytes.SplitN(record, []byte{' '}, 9)
		if len(fields) != 9 || !bytes.Equal(fields[0], []byte("1")) || !validOrdinaryStatusPair(fields[1]) {
			return errStatusAggregateInvalid
		}
		if err := validateStatusTrackedFields(fields[2], fields[3:6], fields[6:8], objectFormat); err != nil {
			return err
		}
		if !validDiscardedStatusPath(fields[8]) {
			return errStatusAggregateInvalid
		}
		return incrementTrackedStatus(aggregate, fields[1])
	case '2':
		fields := bytes.SplitN(record, []byte{' '}, 10)
		if len(fields) != 10 || !bytes.Equal(fields[0], []byte("2")) || !validRenameStatusPair(fields[1]) || !validRenameScore(fields[8]) {
			return errStatusAggregateUnsupported
		}
		if err := validateStatusTrackedFields(fields[2], fields[3:6], fields[6:8], objectFormat); err != nil {
			return err
		}
		originalPath, found, err := reader.next()
		if err != nil || !found || !validDiscardedStatusPath(fields[9]) || !validDiscardedStatusPath(originalPath) || bytes.Equal(fields[9], originalPath) {
			return errStatusAggregateAmbiguous
		}
		return incrementTrackedStatus(aggregate, fields[1])
	case 'u':
		fields := bytes.SplitN(record, []byte{' '}, 11)
		if len(fields) != 11 || !bytes.Equal(fields[0], []byte("u")) || !validUnmergedStatusPair(fields[1]) {
			return errStatusAggregateInvalid
		}
		if err := validateStatusTrackedFields(fields[2], fields[3:7], fields[7:10], objectFormat); err != nil {
			return err
		}
		if !validDiscardedStatusPath(fields[10]) {
			return errStatusAggregateInvalid
		}
		return incrementStatusCount(&aggregate.ConflictCount)
	case '?':
		if len(record) < 3 || record[1] != ' ' || !validDiscardedStatusPath(record[2:]) {
			return errStatusAggregateInvalid
		}
		return incrementStatusCount(&aggregate.UntrackedCount)
	case '!':
		if len(record) < 3 || record[1] != ' ' || !validDiscardedStatusPath(record[2:]) {
			return errStatusAggregateInvalid
		}
		return nil
	default:
		return errStatusAggregateUnsupported
	}
}

func validateStatusTrackedFields(submodule []byte, modes, objectIDs [][]byte, objectFormat string) error {
	if !bytes.Equal(submodule, []byte("N...")) {
		return errStatusAggregateSubmodule
	}
	for _, mode := range modes {
		if bytes.Equal(mode, []byte("160000")) {
			return errStatusAggregateSubmodule
		}
		switch string(mode) {
		case "000000", "100644", "100755", "120000":
		default:
			return errStatusAggregateUnsupported
		}
	}
	for _, objectID := range objectIDs {
		if !validStatusObjectID(objectID, objectFormat) {
			return errStatusAggregateInvalid
		}
	}
	return nil
}

func incrementTrackedStatus(aggregate *StatusAggregate, pair []byte) error {
	if pair[0] != '.' {
		if err := incrementStatusCount(&aggregate.StagedCount); err != nil {
			return err
		}
	}
	if pair[1] != '.' {
		if err := incrementStatusCount(&aggregate.UnstagedCount); err != nil {
			return err
		}
	}
	return nil
}

func incrementStatusCount(count *int) error {
	if count == nil || *count >= statusAggregateCountLimit {
		return errStatusAggregateCountOverflow
	}
	(*count)++
	return nil
}

func validOrdinaryStatusPair(value []byte) bool {
	return validStatusPair(value, ".MTAD", false)
}

func validRenameStatusPair(value []byte) bool {
	return validStatusPair(value, ".MTADR", true) && (value[0] == 'R' || value[1] == 'R')
}

func validStatusPair(value []byte, allowed string, rename bool) bool {
	if len(value) != 2 || value[0] == '.' && value[1] == '.' {
		return false
	}
	for _, status := range value {
		if !strings.ContainsRune(allowed, rune(status)) {
			return false
		}
	}
	return rename || value[0] != 'R' && value[1] != 'R'
}

func validUnmergedStatusPair(value []byte) bool {
	switch string(value) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func validRenameScore(value []byte) bool {
	if len(value) < 2 || len(value) > 4 || value[0] != 'R' {
		return false
	}
	return validStatusDecimal(value[1:], 100)
}

func validStatusAheadBehind(value []byte) bool {
	parts := bytes.Split(value, []byte{' '})
	if len(parts) != 2 || len(parts[0]) < 2 || len(parts[1]) < 2 || parts[0][0] != '+' || parts[1][0] != '-' {
		return false
	}
	ahead, behind := parts[0][1:], parts[1][1:]
	if bytes.Equal(ahead, []byte("?")) || bytes.Equal(behind, []byte("?")) {
		return bytes.Equal(ahead, []byte("?")) && bytes.Equal(behind, []byte("?"))
	}
	return validStatusDecimal(ahead, int(^uint(0)>>1)) && validStatusDecimal(behind, int(^uint(0)>>1))
}

func validStatusDecimal(value []byte, maximum int) bool {
	if len(value) == 0 || len(value) > 1 && value[0] == '0' {
		return false
	}
	number := 0
	for _, digit := range value {
		if digit < '0' || digit > '9' || number > (maximum-int(digit-'0'))/10 {
			return false
		}
		number = number*10 + int(digit-'0')
	}
	return number <= maximum
}

func validStatusObjectID(value []byte, objectFormat string) bool {
	want := 40
	if objectFormat == StatusObjectFormatSHA256 {
		want = 64
	}
	if len(value) != want {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validStatusReference(value []byte, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || !utf8.Valid(value) || bytes.IndexAny(value, "\x00\r\n") >= 0 {
		return false
	}
	text := string(value)
	return text == strings.TrimSpace(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func validDiscardedStatusPath(value []byte) bool {
	return len(value) != 0
}

func validStatusWorkspaceID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func classifyStatusAggregateParse(err error) error {
	switch {
	case errors.Is(err, errStatusAggregateDetached):
		return statusAggregateError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DETACHED", false, err)
	case errors.Is(err, errStatusAggregateUnborn):
		return statusAggregateError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_UNBORN", false, err)
	case errors.Is(err, errStatusAggregateSubmodule):
		return statusAggregateError(foundation.ErrorPermissionDenied, "GIT_STATUS_SUBMODULE_UNSUPPORTED", false, err)
	case errors.Is(err, errStatusAggregateCountOverflow):
		return statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_COUNT_OVERFLOW", false, err)
	case errors.Is(err, errStatusAggregateEncoding):
		return statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_ENCODING_INVALID", false, err)
	case errors.Is(err, errStatusAggregateUnsupported):
		return statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_RECORD_UNSUPPORTED", false, err)
	case errors.Is(err, errStatusAggregateAmbiguous):
		return statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_RESULT_AMBIGUOUS", false, err)
	default:
		return statusAggregateError(foundation.ErrorConsistencyViolation, "GIT_STATUS_RESULT_INVALID", false, err)
	}
}

func classifyStatusAggregateCommand(code string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return statusAggregateError(foundation.ErrorRetryableFailure, "GIT_STATUS_TIMEOUT", true, err)
	}
	if errors.Is(err, context.Canceled) {
		return statusAggregateError(foundation.ErrorNonRetryableFailure, "GIT_STATUS_CANCELLED", false, err)
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) && commandErr.OutputLimitExceeded() {
		return statusAggregateError(foundation.ErrorPermissionDenied, "GIT_STATUS_OUTPUT_TOO_LARGE", false, err)
	}
	var executableErr *exec.Error
	if errors.As(err, &executableErr) || errors.Is(err, os.ErrNotExist) {
		return statusAggregateError(foundation.ErrorDependencyUnavailable, "GIT_COMMAND_UNAVAILABLE", false, err)
	}
	return statusAggregateError(foundation.ErrorDependencyUnavailable, code, false, err)
}

func statusAggregateError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
