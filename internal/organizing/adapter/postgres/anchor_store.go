package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMAnchorStore struct {
	db             *gorm.DB
	uow            foundation.UnitOfWork
	sources        app.SynthesisSourceFence
	verifier       app.AnchorRecommendationVerifier
	executionFence workflowapp.ScopedWorkspaceAnalysisExecutionFence
}

var _ app.AnchorService = (*GORMAnchorStore)(nil)
var _ app.AnchorRecommendations = (*GORMAnchorStore)(nil)
var _ app.AnchorFusionRequestStore = (*GORMAnchorStore)(nil)
var _ app.AnchorFusionRequestReader = (*GORMAnchorStore)(nil)

func NewGORMAnchorStore(pool *platformpostgres.Pool, sources app.SynthesisSourceFence, verifiers ...app.AnchorRecommendationVerifier) (*GORMAnchorStore, error) {
	if pool == nil || isNilInterface(sources) {
		return nil, synthesisUnavailable(errors.New("anchor store dependencies required"))
	}
	db, e := pool.GORM()
	if e != nil {
		return nil, e
	}
	uow, e := pool.UnitOfWork()
	if e != nil {
		return nil, e
	}
	if len(verifiers) > 1 {
		return nil, app.AnchorInvalid()
	}
	var verifier app.AnchorRecommendationVerifier
	if len(verifiers) == 1 {
		verifier = verifiers[0]
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)
	if err != nil {
		return nil, err
	}
	return &GORMAnchorStore{db: db, uow: uow, sources: sources, verifier: verifier, executionFence: fence}, nil
}
func (s *GORMAnchorStore) within(ctx context.Context, write bool, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	if s == nil || s.db == nil || s.uow == nil || ctx == nil {
		return app.AnchorInvalid()
	}
	opts := foundation.TransactionOptions{}
	if !write {
		opts.ReadOnly = true
		opts.Isolation = foundation.TransactionIsolationRepeatableRead
	}
	return synthesisDBError(ctx, s.uow.Within(ctx, opts, func(c context.Context, scope foundation.TransactionScope) error {
		tx, e := platformpostgres.GORMTransaction(scope)
		if e != nil {
			return e
		}
		return work(c, scope, tx.WithContext(c))
	}))
}
func anchorNotFound() error {
	return foundation.NewError(foundation.ErrorNotFound, "ANCHOR_NOT_FOUND", false, errors.New("anchor resource does not exist"))
}
func readAnchor(tx *gorm.DB, w, id foundation.ID, lock bool) (domain.Anchor, error) {
	var row anchorModel
	q := tx.Where("workspace_id=? AND id=?", string(w), string(id))
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&row).Error; e != nil {
		if gormNoRows(e) {
			return domain.Anchor{}, anchorNotFound()
		}
		return domain.Anchor{}, e
	}
	var scope anchorScopeModel
	if e := tx.Where("anchor_id=? AND workspace_id=? AND version=?", row.ID, row.WorkspaceID, row.ScopeVersion).Take(&scope).Error; e != nil {
		return domain.Anchor{}, e
	}
	a := domain.Anchor{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), NoteID: foundation.ID(row.NoteID), BasisRevisionID: foundation.ID(row.BasisRevisionID), Title: row.Title, ScopeVersion: row.ScopeVersion, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if e := json.Unmarshal(scope.Scope, &a.Scope); e != nil {
		return a, e
	}
	return a, a.Validate()
}

// 请求锁先串行化重试，再检查当前状态。优先查询不可变回执，使成功命令在后续更新后仍可有效重放。
func lockAnchorReceipt(tx *gorm.DB, w foundation.ID, key, op string, input any, out any) (bool, error) {
	if e := app.ValidateAnchorCommand(w, key); e != nil {
		return false, e
	}
	if e := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "anchor-receipt:"+string(w)+":"+key).Error; e != nil {
		return false, e
	}
	b, e := json.Marshal(input)
	if e != nil {
		return false, e
	}
	h := sha256.Sum256(b)
	var r anchorReceiptModel
	e = tx.Where("workspace_id=? AND idempotency_key=?", string(w), key).Take(&r).Error
	if gormNoRows(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if r.Operation != op || r.RequestHash != hex.EncodeToString(h[:]) {
		return false, app.AnchorConflict()
	}
	return true, json.Unmarshal(r.Result, out)
}
func saveAnchorReceipt(tx *gorm.DB, w, id foundation.ID, key, op string, input, out any, now time.Time) error {
	b, e := json.Marshal(input)
	if e != nil {
		return e
	}
	h := sha256.Sum256(b)
	result, e := encodeAnchor(out)
	if e != nil {
		return e
	}
	return tx.Create(&anchorReceiptModel{WorkspaceID: string(w), AnchorID: string(id), IdempotencyKey: key, Operation: op, RequestHash: hex.EncodeToString(h[:]), Result: result, CreatedAt: now}).Error
}
func (s *GORMAnchorStore) CreateAnchor(ctx context.Context, c app.CreateAnchorCommand) (app.AnchorMutationResult, error) {
	var out app.AnchorMutationResult
	if err := c.Validate(); err != nil {
		return out, err
	}

	e := s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replay, e := lockAnchorReceipt(tx, c.WorkspaceID, c.IdempotencyKey, "CREATE", c, &out)
		if e != nil || replay {
			out.Replayed = replay
			return e
		}
		note, e := loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true)
		if e != nil {
			return e
		}
		if note.Version != c.ExpectedNoteVersion || note.CurrentRevisionID != c.BasisRevisionID {
			return app.AnchorConflict()
		}
		revision, e := loadSynthesisRevision(tx, c.WorkspaceID, c.NoteID, c.BasisRevisionID)
		if e != nil {
			return e
		}
		if revision.NoteID != c.NoteID {
			return app.AnchorConflict()
		}
		var n int64
		if e = tx.Model(&anchorModel{}).Where("workspace_id=? AND note_id=?", string(c.WorkspaceID), string(c.NoteID)).Count(&n).Error; e != nil {
			return e
		}
		if n != 0 {
			return app.AnchorConflict()
		}
		id, e := foundation.NewUUIDGenerator(nil).New()
		if e != nil {
			return e
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		a := domain.Anchor{ID: id, WorkspaceID: c.WorkspaceID, NoteID: c.NoteID, BasisRevisionID: c.BasisRevisionID, Title: c.Title, Scope: c.Scope, ScopeVersion: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
		if e = a.Validate(); e != nil {
			return e
		}
		if e = tx.Create(&anchorModel{ID: string(id), WorkspaceID: string(c.WorkspaceID), NoteID: string(c.NoteID), BasisRevisionID: string(c.BasisRevisionID), Title: a.Title, ScopeVersion: 1, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; e != nil {
			return e
		}
		scope, e := encodeAnchor(c.Scope)
		if e != nil {
			return e
		}
		if e = tx.Create(&anchorScopeModel{AnchorID: string(id), WorkspaceID: string(c.WorkspaceID), Version: 1, Scope: scope, CreatedAt: now}).Error; e != nil {
			return e
		}
		out.Anchor, e = readAnchor(tx, c.WorkspaceID, id, false)
		if e != nil {
			return e
		}
		return saveAnchorReceipt(tx, c.WorkspaceID, id, c.IdempotencyKey, "CREATE", c, out, now)
	})
	return out, e
}
func (s *GORMAnchorStore) GetAnchor(ctx context.Context, w, id foundation.ID) (domain.Anchor, error) {
	var out domain.Anchor
	if !validID(w) || !validID(id) {
		return out, app.AnchorInvalid()
	}
	e := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var e error
		out, e = readAnchor(tx, w, id, false)
		return e
	})
	return out, e
}
func (s *GORMAnchorStore) ListAnchors(ctx context.Context, q app.AnchorListQuery) (app.AnchorPage, error) {
	out := app.AnchorPage{Items: []domain.Anchor{}}
	if !validID(q.WorkspaceID) || q.Limit < 1 || q.Limit > 100 || q.AfterID != "" && !validID(q.AfterID) || q.NoteID != "" && !validID(q.NoteID) {
		return out, app.AnchorInvalid()
	}
	e := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		query := tx.Where("workspace_id=?", string(q.WorkspaceID))
		if q.AfterID != "" {
			query = query.Where("id>?", string(q.AfterID))
		}
		if q.NoteID != "" {
			query = query.Where("note_id=?", string(q.NoteID))
		}
		var rows []anchorModel
		if e := query.Order("id").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			id := foundation.ID(rows[len(rows)-1].ID)
			out.NextAfterID = &id
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if len(ids) == 0 {
			return nil
		}
		var scopes []anchorScopeModel
		if e := tx.Table("organizing.anchor_scope_revision AS s").Select("s.*").Joins("JOIN organizing.knowledge_anchor a ON a.id=s.anchor_id AND a.workspace_id=s.workspace_id AND a.scope_version=s.version").Where("a.workspace_id=? AND a.id IN ?", string(q.WorkspaceID), ids).Find(&scopes).Error; e != nil {
			return e
		}
		byID := map[string]domain.AnchorScope{}
		for _, scope := range scopes {
			var v domain.AnchorScope
			if e := json.Unmarshal(scope.Scope, &v); e != nil {
				return e
			}
			byID[scope.AnchorID] = v
		}
		for _, r := range rows {
			a := domain.Anchor{ID: foundation.ID(r.ID), WorkspaceID: foundation.ID(r.WorkspaceID), NoteID: foundation.ID(r.NoteID), BasisRevisionID: foundation.ID(r.BasisRevisionID), Title: r.Title, Scope: byID[r.ID], ScopeVersion: r.ScopeVersion, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
			if e := a.Validate(); e != nil {
				return e
			}
			out.Items = append(out.Items, a)
		}
		return nil
	})
	return out, e
}
func loadAnchorProposal(tx *gorm.DB, w, anchor, id foundation.ID) (domain.AnchorProposal, error) {
	var row anchorProposalModel
	if e := tx.Where("workspace_id=? AND anchor_id=? AND id=?", string(w), string(anchor), string(id)).Take(&row).Error; e != nil {
		if gormNoRows(e) {
			return domain.AnchorProposal{}, anchorNotFound()
		}
		return domain.AnchorProposal{}, e
	}
	p := domain.AnchorProposal{ID: id, WorkspaceID: w, AnchorID: anchor, Kind: row.Kind, ScopeVersion: row.ScopeVersion, Reason: row.Reason, ModelRunID: foundation.ID(row.ModelRunID), Status: domain.AnchorPending, Version: 1, CreatedAt: row.CreatedAt, Evidence: []domain.SynthesisSourceRef{}}
	if p.Kind == domain.AnchorScopeAdjustment {
		if row.Suggested == nil {
			return p, app.AnchorConflict()
		}
		var before anchorScopeModel
		if e := tx.Where("workspace_id=? AND anchor_id=? AND version=?", string(w), string(anchor), row.ScopeVersion).Take(&before).Error; e != nil {
			return p, e
		}
		if e := json.Unmarshal(before.Scope, &p.Before); e != nil {
			return p, e
		}
		if e := json.Unmarshal(*row.Suggested, &p.Suggested); e != nil {
			return p, e
		}
	}
	var evidence []anchorEvidenceModel
	if e := tx.Where("workspace_id=? AND proposal_id=?", string(w), string(id)).Order("source_span_id").Find(&evidence).Error; e != nil {
		return p, e
	}
	for _, ref := range evidence {
		p.Evidence = append(p.Evidence, ref.ref())
	}
	var decision anchorDecisionModel
	e := tx.Where("workspace_id=? AND proposal_id=?", string(w), string(id)).Take(&decision).Error
	if e == nil {
		p.Status = domain.AnchorDecision(decision.Decision)
		p.Version = 2
	} else if !gormNoRows(e) {
		return p, e
	}
	return p, p.Validate(w)
}
func (s *GORMAnchorStore) ListAnchorProposals(ctx context.Context, q app.AnchorProposalQuery) (app.AnchorProposalPage, error) {
	out := app.AnchorProposalPage{Items: []domain.AnchorProposal{}}
	if !validID(q.WorkspaceID) || !validID(q.AnchorID) || q.Limit < 1 || q.Limit > 100 || q.AfterID != "" && !validID(q.AfterID) || (q.Kind != domain.AnchorScopeAdjustment && q.Kind != domain.AnchorSourceAssociation) {
		return out, app.AnchorInvalid()
	}
	e := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if _, e := readAnchor(tx, q.WorkspaceID, q.AnchorID, false); e != nil {
			return e
		}
		query := tx.Where("workspace_id=? AND anchor_id=? AND kind=?", string(q.WorkspaceID), string(q.AnchorID), q.Kind)
		if q.AfterID != "" {
			query = query.Where("id>?", string(q.AfterID))
		}
		var rows []anchorProposalModel
		if e := query.Order("id").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			id := foundation.ID(rows[len(rows)-1].ID)
			out.NextAfterID = &id
		}
		ids := make([]string, len(rows))
		versions := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
			versions[i] = r.ScopeVersion
		}
		if len(ids) == 0 {
			return nil
		}
		var refs []anchorEvidenceModel
		if e := tx.Where("workspace_id=? AND proposal_id IN ?", string(q.WorkspaceID), ids).Order("source_span_id").Find(&refs).Error; e != nil {
			return e
		}
		byRef := map[string][]domain.SynthesisSourceRef{}
		for _, r := range refs {
			byRef[r.ProposalID] = append(byRef[r.ProposalID], r.ref())
		}
		var decisions []anchorDecisionModel
		if e := tx.Where("workspace_id=? AND proposal_id IN ?", string(q.WorkspaceID), ids).Find(&decisions).Error; e != nil {
			return e
		}
		byDecision := map[string]string{}
		for _, d := range decisions {
			byDecision[d.ProposalID] = d.Decision
		}
		byScope := map[int64]domain.AnchorScope{}
		if q.Kind == domain.AnchorScopeAdjustment {
			var scopes []anchorScopeModel
			if e := tx.Where("workspace_id=? AND anchor_id=? AND version IN ?", string(q.WorkspaceID), string(q.AnchorID), versions).Find(&scopes).Error; e != nil {
				return e
			}
			for _, sc := range scopes {
				var v domain.AnchorScope
				if e := json.Unmarshal(sc.Scope, &v); e != nil {
					return e
				}
				byScope[sc.Version] = v
			}
		}
		for _, r := range rows {
			p := domain.AnchorProposal{ID: foundation.ID(r.ID), WorkspaceID: q.WorkspaceID, AnchorID: q.AnchorID, Kind: r.Kind, ScopeVersion: r.ScopeVersion, Reason: r.Reason, ModelRunID: foundation.ID(r.ModelRunID), Status: domain.AnchorPending, Version: 1, CreatedAt: r.CreatedAt, Evidence: byRef[r.ID]}
			if d := byDecision[r.ID]; d != "" {
				p.Status = domain.AnchorDecision(d)
				p.Version = 2
			}
			if p.Kind == domain.AnchorScopeAdjustment {
				if r.Suggested == nil {
					return app.AnchorConflict()
				}
				before := byScope[r.ScopeVersion]
				p.Before = &before
				if e := json.Unmarshal(*r.Suggested, &p.Suggested); e != nil {
					return e
				}
			}
			if e := p.Validate(q.WorkspaceID); e != nil {
				return e
			}
			out.Items = append(out.Items, p)
		}
		return nil
	})
	return out, e
}
func (s *GORMAnchorStore) RecordAnchorRecommendation(ctx context.Context, c app.RecordAnchorRecommendation) (app.AnchorRecommendationResult, error) {
	var out app.AnchorRecommendationResult
	if app.ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !validID(c.AnchorID) || c.ExpectedScopeVersion < 1 {
		return out, app.AnchorInvalid()
	}
	e := s.within(ctx, true, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		replay, e := lockAnchorReceipt(tx, c.WorkspaceID, c.IdempotencyKey, "RECOMMEND", c, &out)
		if e != nil || replay {
			out.Replayed = replay
			return e
		}
		a, e := readAnchor(tx, c.WorkspaceID, c.AnchorID, true)
		if e != nil {
			return e
		}
		if a.ScopeVersion != c.ExpectedScopeVersion {
			return app.AnchorConflict()
		}
		id, e := foundation.NewUUIDGenerator(nil).New()
		if e != nil {
			return e
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		p := domain.AnchorProposal{ID: id, WorkspaceID: c.WorkspaceID, AnchorID: c.AnchorID, Kind: c.Kind, ScopeVersion: c.ExpectedScopeVersion, Suggested: c.Suggested, Reason: c.Reason, Evidence: c.Evidence, ModelRunID: c.ModelRunID, Status: domain.AnchorPending, Version: 1, CreatedAt: now}
		if c.Kind == domain.AnchorScopeAdjustment {
			p.Before = &a.Scope
			if reflect.DeepEqual(p.Before, p.Suggested) {
				return app.AnchorInvalid()
			}
		}
		if e = p.Validate(c.WorkspaceID); e != nil {
			return e
		}
		var count int64
		if e = tx.Table("agent.model_run").Where("workspace_id=? AND id=? AND status=?", string(c.WorkspaceID), string(c.ModelRunID), "SUCCEEDED").Count(&count).Error; e != nil {
			return e
		}
		if count != 1 {
			return app.AnchorConflict()
		}
		if isNilInterface(s.verifier) {
			return synthesisUnavailable(errors.New("anchor recommendation verifier is not configured"))
		}
		if c.RequestID != "" {
			row, err := loadAnchorRecommendationRequest(tx, c.WorkspaceID, c.RequestID, true)
			if err != nil {
				return err
			}
			if err := s.verifyLiveAnchorRecommendation(ctx, scope, tx, row); err != nil {
				return err
			}
		}
		if e = s.verifier.VerifyAnchorRecommendationScoped(ctx, scope, a, c); e != nil {
			return e
		}
		if e = s.sources.VerifySynthesisSourcesScoped(ctx, scope, c.WorkspaceID, c.Evidence); e != nil {
			return e
		}
		var suggested *organizingJSONB
		if c.Suggested != nil {
			encoded, err := encodeAnchor(c.Suggested)
			e = err
			suggested = &encoded
			if e != nil {
				return e
			}
		}
		if e = tx.Create(&anchorProposalModel{ID: string(id), WorkspaceID: string(c.WorkspaceID), AnchorID: string(c.AnchorID), Kind: c.Kind, ScopeVersion: c.ExpectedScopeVersion, Suggested: suggested, Reason: c.Reason, ModelRunID: string(c.ModelRunID), CreatedAt: now}).Error; e != nil {
			return e
		}
		refs := make([]anchorEvidenceModel, len(c.Evidence))
		for i, r := range c.Evidence {
			refs[i] = anchorEvidenceModel{WorkspaceID: string(c.WorkspaceID), AnchorID: string(c.AnchorID), ProposalID: string(id), SourceID: string(r.Source.SourceID), SourceVersionID: string(r.Source.SourceVersionID), ContentArtifactID: string(r.Source.ContentArtifactID), ParseProjectionID: string(r.Source.ParseProjectionID), SourceSpanID: string(r.SourceSpanID), ContentHash: r.Source.ContentHash, ExcerptHash: r.ExcerptHash, Title: r.Title}
		}
		if e = tx.Create(&refs).Error; e != nil {
			return e
		}
		if c.RequestID != "" {
			output := append([]byte(nil), c.ModelOutput...)
			updated := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND id=? AND status=?", string(c.WorkspaceID), string(c.RequestID), string(domain.AnchorRecommendationRunning)).Updates(map[string]any{"status": string(domain.AnchorRecommendationSucceeded), "model_run_id": string(c.ModelRunID), "model_output": output, "proposal_id": string(id), "version": gorm.Expr("version+1"), "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return app.AnchorConflict()
			}
		}
		out.Proposal, e = loadAnchorProposal(tx, c.WorkspaceID, c.AnchorID, id)
		if e != nil {
			return e
		}
		return saveAnchorReceipt(tx, c.WorkspaceID, c.AnchorID, c.IdempotencyKey, "RECOMMEND", c, out, now)
	})
	return out, e
}
func (s *GORMAnchorStore) DecideAnchor(ctx context.Context, c app.DecideAnchorCommand) (app.AnchorDecisionResult, error) {
	out := app.AnchorDecisionResult{Items: []domain.AnchorProposal{}}
	if err := c.Validate(); err != nil {
		return out, err
	}

	// 路由字段标记为 json:"-"，需显式纳入不可变绑定。
	binding := struct {
		AnchorID foundation.ID
		Kind     string
		Command  app.DecideAnchorCommand
	}{c.AnchorID, c.Kind, c}
	e := s.within(ctx, true, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		replay, e := lockAnchorReceipt(tx, c.WorkspaceID, c.IdempotencyKey, "DECIDE", binding, &out)
		if e != nil || replay {
			out.Replayed = replay
			return e
		}
		a, e := readAnchor(tx, c.WorkspaceID, c.AnchorID, true)
		if e != nil {
			return e
		}
		if a.Version != c.ExpectedAnchorVersion {
			return app.AnchorConflict()
		}
		proposals := make([]domain.AnchorProposal, len(c.Items))
		refs := []domain.SynthesisSourceRef{}
		for i, item := range c.Items {
			p, e := loadAnchorProposal(tx, c.WorkspaceID, c.AnchorID, item.ProposalID)
			if e != nil {
				return e
			}
			if p.Kind != c.Kind || p.Status != domain.AnchorPending || p.Version != item.ExpectedVersion || p.ScopeVersion != a.ScopeVersion {
				return app.AnchorConflict()
			}
			proposals[i] = p
			refs = append(refs, p.Evidence...)
		}
		if c.Decision == domain.AnchorAccepted {
			if e = s.verifyAnchorSources(ctx, scope, c.WorkspaceID, refs); e != nil {
				return e
			}
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		nextScope := a.ScopeVersion
		if c.Kind == domain.AnchorScopeAdjustment && c.Decision == domain.AnchorAccepted {
			p := proposals[0]
			value, e := encodeAnchor(p.Suggested)
			if e != nil {
				return e
			}
			pid := string(p.ID)
			nextScope++
			if e = tx.Create(&anchorScopeModel{AnchorID: string(a.ID), WorkspaceID: string(a.WorkspaceID), Version: nextScope, Scope: value, ProposalID: &pid, CreatedAt: now}).Error; e != nil {
				return e
			}
		}
		decisions := make([]anchorDecisionModel, len(proposals))
		for i, p := range proposals {
			decisions[i] = anchorDecisionModel{ProposalID: string(p.ID), WorkspaceID: string(a.WorkspaceID), AnchorID: string(a.ID), Decision: string(c.Decision), AnchorVersion: a.Version + 1, ReceiptKey: c.IdempotencyKey, CreatedAt: now}
		}
		if e = tx.Create(&decisions).Error; e != nil {
			return e
		}
		changed := tx.Model(&anchorModel{}).Where("workspace_id=? AND id=? AND version=?", string(a.WorkspaceID), string(a.ID), a.Version).Updates(map[string]any{"version": a.Version + 1, "scope_version": nextScope, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		out.Anchor, e = readAnchor(tx, a.WorkspaceID, a.ID, false)
		if e != nil {
			return e
		}
		for _, p := range proposals {
			updated, e := loadAnchorProposal(tx, a.WorkspaceID, a.ID, p.ID)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, updated)
		}
		if c.Kind == domain.AnchorSourceAssociation && c.Decision == domain.AnchorAccepted {
			if e = appendAnchorFusionRequests(tx, a, proposals, now); e != nil {
				return e
			}
		}
		return saveAnchorReceipt(tx, a.WorkspaceID, a.ID, c.IdempotencyKey, "DECIDE", binding, out, now)
	})
	return out, e
}

func appendAnchorFusionRequests(tx *gorm.DB, anchor domain.Anchor, proposals []domain.AnchorProposal, now time.Time) error {
	for _, proposal := range proposals {
		groups := map[domain.SynthesisSourceVersion][]domain.SynthesisSourceRef{}
		for _, ref := range proposal.Evidence {
			groups[ref.Source] = append(groups[ref.Source], ref)
		}
		for source, refs := range groups {
			var event struct {
				ID                 string
				IngestionAttemptID string    `gorm:"column:ingestion_attempt_id"`
				OccurredAt         time.Time `gorm:"column:occurred_at"`
			}
			q := tx.Raw(`SELECT id::text,payload->>'ingestion_attempt_id' AS ingestion_attempt_id,occurred_at FROM workflow.outbox_event
                WHERE workspace_id=? AND event_type='ingestion.source.ready' AND payload->>'source_id'=? AND payload->>'source_version_id'=?
                  AND payload->>'content_artifact_id'=? AND payload->>'parse_projection_id'=? AND payload->>'content_hash'=? ORDER BY occurred_at DESC,id DESC LIMIT 1`, string(anchor.WorkspaceID), string(source.SourceID), string(source.SourceVersionID), string(source.ContentArtifactID), string(source.ParseProjectionID), source.ContentHash).Scan(&event)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return app.AnchorConflict()
			}
			id, err := foundation.NewUUIDGenerator(nil).New()
			if err != nil {
				return err
			}
			allowed, err := encodeAnchor(refs)
			if err != nil {
				return err
			}
			row := anchorFusionRequestModel{ID: string(id), WorkspaceID: string(anchor.WorkspaceID), AnchorID: string(anchor.ID), NoteID: string(anchor.NoteID), ProposalID: string(proposal.ID), ScopeVersion: anchor.ScopeVersion, SourceEventID: event.ID, SourceID: string(source.SourceID), SourceVersionID: string(source.SourceVersionID), ContentArtifactID: string(source.ContentArtifactID), ParseProjectionID: string(source.ParseProjectionID), SourceContentHash: source.ContentHash, IngestionAttemptID: event.IngestionAttemptID, SourceOccurredAt: event.OccurredAt, AllowedSources: allowed, Status: "PENDING", CreatedAt: now, UpdatedAt: now}
			if err = tx.Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *GORMAnchorStore) ClaimAnchorFusionRequestScoped(ctx context.Context, scope foundation.TransactionScope, excluded []foundation.ID) (app.AnchorFusionRequest, bool, error) {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.AnchorFusionRequest{}, false, err
	}
	q := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status=?", "PENDING").Order("created_at,id").Limit(1)
	if len(excluded) > 0 {
		ids := make([]string, len(excluded))
		for i, id := range excluded {
			ids[i] = string(id)
		}
		q = q.Where("workspace_id NOT IN ?", ids)
	}
	var row anchorFusionRequestModel
	if err = q.Take(&row).Error; err != nil {
		if gormNoRows(err) {
			return app.AnchorFusionRequest{}, false, nil
		}
		return app.AnchorFusionRequest{}, false, err
	}
	request, err := fusionRequestProjection(row)
	if err != nil {
		return app.AnchorFusionRequest{}, false, err
	}
	return request, true, nil
}

func (s *GORMAnchorStore) MarkAnchorFusionRequestStartedScoped(ctx context.Context, scope foundation.TransactionScope, requestID, processingID foundation.ID) error {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	updated := tx.WithContext(ctx).Model(&anchorFusionRequestModel{}).Where("id=? AND status='PENDING' AND processing_id IS NULL", string(requestID)).Updates(map[string]any{"status": "DISPATCHED", "processing_id": string(processingID), "updated_at": gorm.Expr("clock_timestamp()")})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return app.AnchorConflict()
	}
	return nil
}
func (s *GORMAnchorStore) MarkAnchorFusionRequestStaleScoped(ctx context.Context, scope foundation.TransactionScope, requestID foundation.ID) error {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	updated := tx.WithContext(ctx).Model(&anchorFusionRequestModel{}).Where("id=? AND status='PENDING'", string(requestID)).Updates(map[string]any{"status": "STALE", "updated_at": gorm.Expr("clock_timestamp()")})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return app.AnchorConflict()
	}
	return nil
}
func fusionRequestProjection(row anchorFusionRequestModel) (app.AnchorFusionRequest, error) {
	var refs []domain.SynthesisSourceRef
	if err := json.Unmarshal(row.AllowedSources, &refs); err != nil || len(refs) == 0 {
		return app.AnchorFusionRequest{}, app.AnchorInvalid()
	}
	for _, ref := range refs {
		if ref.Validate() != nil {
			return app.AnchorFusionRequest{}, app.AnchorInvalid()
		}
	}
	event := domain.SynthesisSourceReady{ID: foundation.ID(row.SourceEventID), Source: domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(row.WorkspaceID), SourceID: foundation.ID(row.SourceID), SourceVersionID: foundation.ID(row.SourceVersionID), ContentArtifactID: foundation.ID(row.ContentArtifactID), ParseProjectionID: foundation.ID(row.ParseProjectionID), ContentHash: row.SourceContentHash}, IngestionAttemptID: foundation.ID(row.IngestionAttemptID), ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: row.SourceOccurredAt.UTC().Truncate(time.Microsecond)}
	if event.Validate() != nil {
		return app.AnchorFusionRequest{}, event.Validate()
	}
	processingID := foundation.ID("")
	if row.ProcessingID != nil {
		processingID = foundation.ID(*row.ProcessingID)
	}
	return app.AnchorFusionRequest{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), AnchorID: foundation.ID(row.AnchorID), NoteID: foundation.ID(row.NoteID), ProposalID: foundation.ID(row.ProposalID), ScopeVersion: row.ScopeVersion, SourceEvent: event, AllowedSources: refs, ProcessingID: processingID, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (s *GORMAnchorStore) GetAnchorFusionRequest(ctx context.Context, workspaceID, requestID foundation.ID) (app.AnchorFusionRequest, error) {
	if !validID(workspaceID) || !validID(requestID) {
		return app.AnchorFusionRequest{}, app.AnchorInvalid()
	}
	var row anchorFusionRequestModel
	if err := s.db.WithContext(ctx).Where("workspace_id=? AND id=?", string(workspaceID), string(requestID)).Take(&row).Error; err != nil {
		if gormNoRows(err) {
			return app.AnchorFusionRequest{}, anchorNotFound()
		}
		return app.AnchorFusionRequest{}, err
	}
	return fusionRequestProjection(row)
}
func (s *GORMAnchorStore) ListAnchorFusionRequests(ctx context.Context, workspaceID, anchorID foundation.ID, limit int) ([]app.AnchorFusionRequest, error) {
	if !validID(workspaceID) || !validID(anchorID) || limit < 1 || limit > 100 {
		return nil, app.AnchorInvalid()
	}
	var rows []anchorFusionRequestModel
	if err := s.db.WithContext(ctx).Where("workspace_id=? AND anchor_id=?", string(workspaceID), string(anchorID)).Order("created_at DESC,id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]app.AnchorFusionRequest, 0, len(rows))
	for _, row := range rows {
		v, e := fusionRequestProjection(row)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// 限制每次所属模块调用的规模，同时保持全局稳定的来源加锁顺序。
func (s *GORMAnchorStore) verifyAnchorSources(ctx context.Context, scope foundation.TransactionScope, w foundation.ID, refs []domain.SynthesisSourceRef) error {
	ordered := append([]domain.SynthesisSourceRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Source.SourceID != b.Source.SourceID {
			return a.Source.SourceID < b.Source.SourceID
		}
		if a.Source.SourceVersionID != b.Source.SourceVersionID {
			return a.Source.SourceVersionID < b.Source.SourceVersionID
		}
		return a.SourceSpanID < b.SourceSpanID
	})
	for start := 0; start < len(ordered); start += domain.MaxSynthesisSources {
		end := start + domain.MaxSynthesisSources
		if end > len(ordered) {
			end = len(ordered)
		}
		if e := s.sources.VerifySynthesisSourcesScoped(ctx, scope, w, ordered[start:end]); e != nil {
			return e
		}
	}
	return nil
}
