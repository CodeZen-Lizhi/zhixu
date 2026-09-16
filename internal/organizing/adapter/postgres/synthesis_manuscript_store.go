package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changeapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxManuscriptRecordBytes = 16 << 20

type SynthesisManuscriptStoreDependencies struct {
	Baselines app.SynthesisManuscriptBaselineReader
	Proof     app.SynthesisManuscriptProof
	Files     app.SynthesisManuscriptFileReader
	Mapper    domain.SynthesisManuscriptMapper
	Merge     changeapp.RevisionMergeEngine
	IDs       foundation.IDGenerator
	Clock     foundation.Clock
}

// GORMSynthesisManuscriptStore 暂未组装进生产执行器；Proof 不能为 nil，也不能用客户端断言替代。
type GORMSynthesisManuscriptStore struct {
	db           *gorm.DB
	uow          foundation.UnitOfWork
	dependencies SynthesisManuscriptStoreDependencies
}

var _ app.SynthesisManuscriptStore = (*GORMSynthesisManuscriptStore)(nil)

func NewGORMSynthesisManuscriptStore(pool *platformpostgres.Pool, d SynthesisManuscriptStoreDependencies) (*GORMSynthesisManuscriptStore, error) {
	if pool == nil || isNilInterface(d.Baselines) || isNilInterface(d.Proof) || isNilInterface(d.Files) || isNilInterface(d.Mapper) || isNilInterface(d.Merge) || isNilInterface(d.IDs) || isNilInterface(d.Clock) {
		return nil, manuscriptStoreInvalid("manuscript owner dependencies are required")
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &GORMSynthesisManuscriptStore{db: db, uow: uow, dependencies: d}, nil
}

// 载荷字节精确保留规范 Go JSON，包括 UTF-8 和哈希输入；SQL 校验仅为检查结构绑定而将这些字节投影为 jsonb。
type manuscriptCaptureRow struct {
	ID          string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id;type:uuid"`
	Payload     []byte `gorm:"column:payload;type:bytea"`
	PayloadHash string `gorm:"column:payload_hash"`
}

func (manuscriptCaptureRow) TableName() string { return "organizing.synthesis_manuscript_capture" }

type manuscriptAttemptRow struct {
	ID             string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string `gorm:"column:workspace_id;type:uuid"`
	CaptureID      string `gorm:"column:capture_id;type:uuid"`
	IdempotencyKey string `gorm:"column:idempotency_key"`
	Payload        []byte `gorm:"column:payload;type:bytea"`
	PayloadHash    string `gorm:"column:payload_hash"`
}

func (manuscriptAttemptRow) TableName() string { return "organizing.synthesis_manuscript_attempt" }

type manuscriptReceiptRow struct {
	ID          string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id;type:uuid"`
	AttemptID   string `gorm:"column:attempt_id;type:uuid"`
	Payload     []byte `gorm:"column:payload;type:bytea"`
	PayloadHash string `gorm:"column:payload_hash"`
}

func (manuscriptReceiptRow) TableName() string { return "organizing.synthesis_manuscript_receipt" }

func (s *GORMSynthesisManuscriptStore) Prepare(ctx context.Context, command app.PrepareSynthesisManuscript) (app.SynthesisManuscriptAttempt, error) {
	var out app.SynthesisManuscriptAttempt
	if err := validateManuscriptCommand(command); err != nil {
		return out, err
	}
	// 精确重试恢复原始快照，即使文件或所属模块状态已变化。
	var prior manuscriptAttemptRow
	err := s.db.WithContext(ctx).Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&prior).Error
	if err == nil {
		return s.recoverAttempt(ctx, s.db, prior, command)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return out, synthesisDBError(ctx, err)
	}
	prepared, err := s.dependencies.Baselines.LoadSynthesisManuscriptBaseline(ctx, command)
	if err != nil {
		return out, err
	}
	if prepared.MergeInput.FileExists || prepared.MergeInput.FileContent != "" {
		return out, manuscriptStoreInvalid("baseline reader cannot supply captured file bytes")
	}
	capture := app.SynthesisManuscriptCapture{WorkspaceID: command.WorkspaceID, TargetPath: prepared.Authority.TargetPath, RootGrantID: prepared.Authority.RootGrantID, RootFingerprint: prepared.Authority.RootFingerprint, WorkspaceBindingVersion: prepared.Authority.WorkspaceBindingVersion, CreatedAt: s.now(), Bytes: []byte{}}
	capture.ID, err = s.dependencies.IDs.New()
	if err != nil {
		return out, err
	}
	if err = validateManuscriptPrepared(command, prepared); err != nil {
		return out, err
	}
	// 读取任何文件字节前，先验证精确的服务端路径和根目录权限。
	err = s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		authority, e := s.dependencies.Proof.VerifySynthesisManuscriptPreparedScoped(ctx, scope, prepared)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(authority, prepared.Authority) {
			return manuscriptStoreConflict("capture authority changed")
		}
		if prepared.MergeInput.Published == nil {
			capture.AbsenceToken, err = authoringdomain.ComputeAbsenceToken(command.WorkspaceID, capture.TargetPath)
			if err == nil {
				err = s.dependencies.Files.EnsureTargetAbsent(ctx, command.WorkspaceID, capture.TargetPath, capture.AbsenceToken)
			}
		} else {
			capture.Exists = true
			capture.Bytes, capture.ContentHash, err = s.dependencies.Files.CurrentContent(ctx, command.WorkspaceID, capture.TargetPath, domain.MaxSynthesisManuscriptBytes)
		}

		return err
	})
	if err != nil {
		return out, err
	}
	if err = validateManuscriptCapture(capture); err != nil {
		return out, err
	}
	prepared.MergeInput.FileExists = capture.Exists
	prepared.MergeInput.FileContent = string(capture.Bytes)
	preview, err := app.PreviewSynthesisManuscript(ctx, prepared.MergeInput, s.dependencies.Merge, s.dependencies.Mapper)
	if err != nil {
		return out, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return out, err
	}
	out = app.SynthesisManuscriptAttempt{ID: id, Command: command, CaptureID: capture.ID, Prepared: prepared, Preview: preview, CreatedAt: s.now()}
	out.Hash, err = manuscriptValueHash(out)
	if err != nil {
		return out, err
	}
	captureBytes, err := encodeManuscriptRecord(capture)
	if err != nil {
		return out, err
	}
	attemptBytes, err := encodeManuscriptRecord(out)
	if err != nil {
		return out, err
	}
	// 尝试精确并发恢复前，保留数据库底层原因。
	err = s.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		tx = tx.WithContext(ctx)
		if err := s.verifyCurrent(ctx, scope, prepared, capture); err != nil {
			return err
		}
		c := manuscriptCaptureRow{ID: string(capture.ID), WorkspaceID: string(command.WorkspaceID), Payload: captureBytes, PayloadHash: manuscriptBytesHash(captureBytes)}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		a := manuscriptAttemptRow{ID: string(out.ID), WorkspaceID: string(command.WorkspaceID), CaptureID: string(capture.ID), IdempotencyKey: command.IdempotencyKey, Payload: attemptBytes, PayloadHash: manuscriptBytesHash(attemptBytes)}
		return tx.Create(&a).Error
	})
	if err != nil {
		// 并发的精确请求可能已胜出；仅恢复其完整绑定。
		if platformpostgres.SQLState(err) == "23505" {
			var row manuscriptAttemptRow
			if e := s.db.WithContext(ctx).Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&row).Error; e == nil {
				return s.recoverAttempt(ctx, s.db, row, command)
			}
		}
		return app.SynthesisManuscriptAttempt{}, synthesisDBError(ctx, err)
	}
	return out, nil
}

func (s *GORMSynthesisManuscriptStore) GetAttempt(ctx context.Context, workspace, id foundation.ID) (app.SynthesisManuscriptAttempt, error) {
	if !validID(workspace) || !validID(id) {
		return app.SynthesisManuscriptAttempt{}, manuscriptStoreInvalid("invalid attempt identity")
	}
	out, _, err := s.readAttempt(ctx, s.db, workspace, id)
	return out, synthesisDBError(ctx, err)
}

func (s *GORMSynthesisManuscriptStore) SealClean(ctx context.Context, workspace, id foundation.ID) (app.SynthesisManuscriptReceipt, error) {
	var out app.SynthesisManuscriptReceipt
	if !validID(workspace) || !validID(id) {
		return out, manuscriptStoreInvalid("invalid attempt identity")
	}
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		// 通过不可变尝试行串行化封存；先执行精确恢复，再执行 CAS。
		var row manuscriptAttemptRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(workspace), string(id)).Take(&row).Error; err != nil {
			return manuscriptReadError(err)
		}
		attempt, capture, err := s.readAttempt(ctx, tx, workspace, id)
		if err != nil {
			return err
		}
		var receipt manuscriptReceiptRow
		err = tx.Where("workspace_id=? AND attempt_id=?", string(workspace), string(id)).Take(&receipt).Error
		if err == nil {
			out, err = s.decodeReceipt(receipt, attempt)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if attempt.Preview.Review != nil || attempt.Preview.Manuscript == nil {
			return manuscriptStoreConflict("conflicted preview requires owner resolution")
		}
		if err = s.verifyCurrent(ctx, scope, attempt.Prepared, capture); err != nil {
			return err
		}
		receiptID, err := s.dependencies.IDs.New()
		if err != nil {
			return err
		}
		out = app.SynthesisManuscriptReceipt{ID: receiptID, WorkspaceID: workspace, AttemptID: id, AttemptHash: attempt.Hash, Manuscript: *attempt.Preview.Manuscript, CreatedAt: s.now()}
		out.Hash, err = manuscriptValueHash(out)
		if err != nil {
			return err
		}
		payload, err := encodeManuscriptRecord(out)
		if err != nil {
			return err
		}
		return tx.Create(&manuscriptReceiptRow{ID: string(out.ID), WorkspaceID: string(workspace), AttemptID: string(id), Payload: payload, PayloadHash: manuscriptBytesHash(payload)}).Error
	})
	if err != nil {
		return app.SynthesisManuscriptReceipt{}, err
	}
	return out, nil
}

func (s *GORMSynthesisManuscriptStore) VerifyReceiptScoped(ctx context.Context, scope foundation.TransactionScope, workspace, receiptID foundation.ID, revision domain.SynthesisRevision) error {
	if !validID(workspace) || !validID(receiptID) || revision.WorkspaceID != workspace || revision.RendererVersion != domain.SynthesisRendererVersionV2 || revision.Validate() != nil {
		return manuscriptStoreInvalid("invalid manuscript revision")
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	var row manuscriptReceiptRow
	if err = tx.Where("workspace_id=? AND id=?", string(workspace), string(receiptID)).Take(&row).Error; err != nil {
		return manuscriptReadError(err)
	}
	attempt, capture, err := s.readAttempt(ctx, tx, workspace, foundation.ID(row.AttemptID))
	if err != nil {
		return err
	}
	receipt, err := s.decodeReceipt(row, attempt)
	if err != nil {
		return err
	}
	if err = validateManuscriptRevisionBinding(revision, attempt, receipt); err != nil {
		return err
	}
	return s.verifyCurrent(ctx, scope, attempt.Prepared, capture)
}

func validateManuscriptRevisionBinding(revision domain.SynthesisRevision, a app.SynthesisManuscriptAttempt, r app.SynthesisManuscriptReceipt) error {
	p := a.Prepared
	l := p.MergeInput.Latest
	if revision.Manuscript == nil || !reflect.DeepEqual(*revision.Manuscript, r.Manuscript) || revision.ParentRevisionID != l.ID || revision.NoteID != l.NoteID || revision.DocumentID != l.DocumentID || revision.RevisionNo != l.RevisionNo+1 || revision.ArticleRevisionNo != l.ArticleRevisionNo+1 || revision.SourceEventID != p.GenerationInput.SourceEvent.ID || revision.WorkflowRunID != p.GenerationInput.WorkflowRunID || revision.ModelRunID != p.Generation.ModelRunID {
		return manuscriptStoreConflict("revision differs from sealed owner proof")
	}
	var matched bool
	for _, note := range p.Generation.Notes {
		if note.NoteID == l.NoteID {
			matched = reflect.DeepEqual(revision.Delta, note.Delta)
		}
	}
	if !matched {
		return manuscriptStoreConflict("revision delta differs from validated generation")
	}
	return nil
}

func (s *GORMSynthesisManuscriptStore) recoverAttempt(ctx context.Context, tx *gorm.DB, row manuscriptAttemptRow, c app.PrepareSynthesisManuscript) (app.SynthesisManuscriptAttempt, error) {
	a, _, err := s.readAttempt(ctx, tx, c.WorkspaceID, foundation.ID(row.ID))
	if err != nil {
		return a, synthesisDBError(ctx, err)
	}
	if a.Command != c {
		return app.SynthesisManuscriptAttempt{}, manuscriptStoreConflict("idempotency key bound to another request")
	}
	return a, nil
}

func (s *GORMSynthesisManuscriptStore) readAttempt(ctx context.Context, tx *gorm.DB, workspace, id foundation.ID) (app.SynthesisManuscriptAttempt, app.SynthesisManuscriptCapture, error) {
	var a app.SynthesisManuscriptAttempt
	var c app.SynthesisManuscriptCapture
	var row manuscriptAttemptRow
	if err := tx.WithContext(ctx).Where("workspace_id=? AND id=?", string(workspace), string(id)).Take(&row).Error; err != nil {
		return a, c, manuscriptReadError(err)
	}
	if err := decodeManuscriptRecord(row.Payload, row.PayloadHash, &a); err != nil {
		return a, c, err
	}
	hash := a.Hash
	a.Hash = ""
	expected, err := manuscriptValueHash(a)
	a.Hash = hash
	if err != nil || hash != expected || a.ID != id || a.CreatedAt.IsZero() || a.CreatedAt.Location() != time.UTC || a.Command.WorkspaceID != workspace || string(a.CaptureID) != row.CaptureID || a.Command.IdempotencyKey != row.IdempotencyKey || validateManuscriptCommand(a.Command) != nil || validateManuscriptPrepared(a.Command, a.Prepared) != nil {
		return a, c, manuscriptStoreConflict("stored attempt binding is invalid")
	}
	var captured manuscriptCaptureRow
	if err = tx.WithContext(ctx).Where("workspace_id=? AND id=?", string(workspace), row.CaptureID).Take(&captured).Error; err != nil {
		return a, c, manuscriptReadError(err)
	}
	if err = decodeManuscriptRecord(captured.Payload, captured.PayloadHash, &c); err != nil {
		return a, c, err
	}
	if c.ID != a.CaptureID || c.WorkspaceID != workspace || validateManuscriptCapture(c) != nil || !captureMatchesPrepared(c, a.Prepared) {
		return a, c, manuscriptStoreConflict("stored capture binding is invalid")
	}
	// 重新运行可信解析器及固定合并契约；绝不将客户端重算哈希的映射或已存储的冲突回退结果视为已验证结果。
	preview, err := app.PreviewSynthesisManuscript(ctx, a.Prepared.MergeInput, s.dependencies.Merge, s.dependencies.Mapper)
	if err != nil {
		return a, c, err
	}
	if !reflect.DeepEqual(preview, a.Preview) {
		return a, c, manuscriptStoreConflict("stored preview differs from frozen merge")
	}
	return a, c, nil
}
func (s *GORMSynthesisManuscriptStore) decodeReceipt(row manuscriptReceiptRow, a app.SynthesisManuscriptAttempt) (app.SynthesisManuscriptReceipt, error) {
	var r app.SynthesisManuscriptReceipt
	if err := decodeManuscriptRecord(row.Payload, row.PayloadHash, &r); err != nil {
		return r, err
	}
	hash := r.Hash
	r.Hash = ""
	expected, err := manuscriptValueHash(r)
	r.Hash = hash
	preview := a.Preview
	if r.Review != nil {
		if r.Review.Version != app.SynthesisManuscriptReviewVersion || len(r.Review.Decisions) == 0 {
			return r, manuscriptStoreConflict("invalid receipt review identity")
		}
		preview, err = s.replayDecisionLedger(context.Background(), a, r.Review.Decisions)
		if err != nil {
			return r, err
		}
	}
	if err != nil || hash != expected || string(r.ID) != row.ID || string(r.WorkspaceID) != row.WorkspaceID || r.CreatedAt.IsZero() || r.CreatedAt.Before(a.CreatedAt) || r.CreatedAt.Location() != time.UTC || r.WorkspaceID != a.Command.WorkspaceID || r.AttemptID != a.ID || r.AttemptHash != a.Hash || preview.Review != nil || preview.Manuscript == nil || !reflect.DeepEqual(r.Manuscript, *preview.Manuscript) {
		return r, manuscriptStoreConflict("stored receipt binding is invalid")
	}
	return r, nil
}
func (s *GORMSynthesisManuscriptStore) verifyCurrent(ctx context.Context, scope foundation.TransactionScope, p app.SynthesisManuscriptPrepared, c app.SynthesisManuscriptCapture) error {
	authority, err := s.dependencies.Proof.VerifySynthesisManuscriptPreparedScoped(ctx, scope, p)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(authority, p.Authority) || !captureMatchesPrepared(c, p) {
		return manuscriptStoreConflict("current owner authority changed")
	}
	if !c.Exists {
		return s.dependencies.Files.EnsureTargetAbsent(ctx, c.WorkspaceID, c.TargetPath, c.AbsenceToken)
	}
	content, hash, err := s.dependencies.Files.CurrentContent(ctx, c.WorkspaceID, c.TargetPath, domain.MaxSynthesisManuscriptBytes)
	if err != nil {
		return err
	}
	if hash != c.ContentHash || !bytes.Equal(content, c.Bytes) {
		return manuscriptStoreConflict("captured file changed")
	}
	return nil
}
func captureMatchesPrepared(c app.SynthesisManuscriptCapture, p app.SynthesisManuscriptPrepared) bool {
	a := p.Authority
	return c.WorkspaceID == p.MergeInput.Latest.WorkspaceID && c.TargetPath == a.TargetPath && c.RootGrantID == a.RootGrantID && c.RootFingerprint == a.RootFingerprint && c.WorkspaceBindingVersion == a.WorkspaceBindingVersion && c.Exists == p.MergeInput.FileExists && string(c.Bytes) == p.MergeInput.FileContent
}
func validateManuscriptCommand(c app.PrepareSynthesisManuscript) error {
	if !validID(c.WorkspaceID) || !validID(c.NoteID) || !validID(c.ProcessingID) || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 200 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || strings.ContainsRune(c.IdempotencyKey, 0) {
		return manuscriptStoreInvalid("invalid manuscript request")
	}
	return nil
}
func validateManuscriptPrepared(c app.PrepareSynthesisManuscript, p app.SynthesisManuscriptPrepared) error {
	a := p.Authority
	l := p.MergeInput.Latest
	if l.Validate() != nil || l.WorkspaceID != c.WorkspaceID || l.NoteID != c.NoteID || p.GenerationInput.ProcessingID != c.ProcessingID || p.GenerationInput.SourceEvent.Source.WorkspaceID != c.WorkspaceID || p.Generation.Validate(p.GenerationInput) != nil || p.MergeInput.NextMachine.Validate() != nil || p.MergeInput.NextMachine.NoteID != l.NoteID || p.MergeInput.NextMachine.WorkspaceID != l.WorkspaceID || !validID(p.SemanticModelRunID) || p.SemanticModelRunID == p.Generation.ModelRunID || !validHash(p.SemanticOutputHash) || a.DocumentID != l.DocumentID || a.LatestArticleID != l.ArticleRevisionID || a.DocumentVersion < 1 || a.NoteVersion < 1 || a.WorkspaceBindingVersion < 1 || !validID(a.RootGrantID) || !validHash(a.RootFingerprint) || !validManuscriptAuthorityAnchor(p) || changedomain.ValidateWorkspaceTarget(c.WorkspaceID, a.TargetPath) != nil {
		return manuscriptStoreInvalid("invalid server manuscript baseline")
	}
	matched := 0
	for _, note := range p.Generation.Notes {
		if note.NoteID == l.NoteID {
			matched++
		}
	}
	if matched != 1 {
		return manuscriptStoreInvalid("baseline lacks exact machine generation")
	}
	if p.MergeInput.Published == nil {
		if a.PublishedPublicationID != "" || a.PublishedProposalCommitID != "" || a.PublishedGitCommit != "" {
			return manuscriptStoreInvalid("unexpected publication identity")
		}
	} else if p.MergeInput.Published.Validate() != nil || !validID(a.PublishedPublicationID) || !validID(a.PublishedProposalCommitID) || len(a.PublishedGitCommit) != 40 || strings.Trim(a.PublishedGitCommit, "0123456789abcdef") != "" {
		return manuscriptStoreInvalid("missing proven publication identity")
	}
	return nil
}

// 只有冻结目标明确没有锚点时，空权限才有效；生产证明通过所属模块重新核验当前仍无锚点。
func validManuscriptAuthorityAnchor(p app.SynthesisManuscriptPrepared) bool {
	// 保留锚点证明适配器的原始存储契约；生产适配器会独立将其与冻结准入比较。
	if p.Authority.Anchor.Validate(p.MergeInput.Latest.WorkspaceID) == nil {
		return true
	}
	for _, note := range p.GenerationInput.Notes {
		if note.Note.ID == p.MergeInput.Latest.NoteID {
			if note.Anchor == nil {
				return reflect.DeepEqual(p.Authority.Anchor, app.SynthesisAnchorBinding{})
			}
			return false
		}
	}
	return false
}

func validateManuscriptCapture(c app.SynthesisManuscriptCapture) error {
	if !validID(c.ID) || !validID(c.WorkspaceID) || !validID(c.RootGrantID) || !validHash(c.RootFingerprint) || c.WorkspaceBindingVersion < 1 || c.CreatedAt.IsZero() || changedomain.ValidateWorkspaceTarget(c.WorkspaceID, c.TargetPath) != nil || len(c.Bytes) > domain.MaxSynthesisManuscriptBytes || !utf8.Valid(c.Bytes) || bytes.IndexByte(c.Bytes, 0) >= 0 {
		return manuscriptStoreInvalid("invalid file capture")
	}
	if c.Exists {
		if c.AbsenceToken != "" || c.ContentHash != manuscriptBytesHash(c.Bytes) {
			return manuscriptStoreInvalid("capture hash mismatch")
		}
	} else {
		expected, err := authoringdomain.ComputeAbsenceToken(c.WorkspaceID, c.TargetPath)
		if err != nil || len(c.Bytes) != 0 || c.ContentHash != "" || c.AbsenceToken != expected {
			return manuscriptStoreInvalid("invalid absence capture")
		}
	}
	return nil
}
func (s *GORMSynthesisManuscriptStore) within(ctx context.Context, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	err := s.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(ctx, scope, tx.WithContext(ctx))
	})
	return synthesisDBError(ctx, err)
}
func (s *GORMSynthesisManuscriptStore) now() time.Time {
	return s.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
}
func manuscriptBytesHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func manuscriptValueHash(v any) (string, error) {
	b, err := encodeManuscriptRecord(v)
	if err != nil {
		return "", err
	}
	return manuscriptBytesHash(b), nil
}
func encodeManuscriptRecord(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(b) > maxManuscriptRecordBytes {
		return nil, manuscriptStoreInvalid("manuscript record exceeds storage bound")
	}
	return b, nil
}
func decodeManuscriptRecord(b []byte, hash string, out any) error {
	if len(b) > maxManuscriptRecordBytes || manuscriptBytesHash(b) != hash {
		return manuscriptStoreConflict("stored manuscript bytes changed")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return manuscriptStoreConflict("stored manuscript encoding is invalid")
	}
	canonical, err := encodeManuscriptRecord(out)
	if err != nil || !bytes.Equal(canonical, b) {
		return manuscriptStoreConflict("stored manuscript encoding is not canonical")
	}
	return nil
}
func manuscriptReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return foundation.NewError(foundation.ErrorNotFound, "SYNTHESIS_MANUSCRIPT_NOT_FOUND", false, errors.New("manuscript record is unavailable"))
	}
	return err
}
func manuscriptStoreInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "SYNTHESIS_MANUSCRIPT_STORE_INVALID", false, errors.New(message))
}
func manuscriptStoreConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_MANUSCRIPT_STALE", false, errors.New(message))
}
