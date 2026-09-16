package postgres

import (
	"context"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// 此所有者投影联结不可变手稿与发布回执。
// 客户端命令不提供回执、P 身份或替换文件的哈希。
type publicationMergeProof struct {
	authoringapp.PublicationMergeBaseline
	FileBase        string
	TargetPath      string
	ContentHash     string
	DocumentVersion int64
	Valid           bool
}

func gormPublicationMergeBaseline(ctx context.Context, tx *gorm.DB, document domain.Document, revision domain.ArticleRevision) (*publicationMergeProof, error) {
	row, err := gormRow(tx.WithContext(ctx), `SELECT COALESCE(receipt_id::text,''),capture_id::text,COALESCE(published_revision_id::text,''),COALESCE(published_content_hash,''),file_base,target_path,content_hash,document_version,valid,COALESCE(historical_republish_id::text,'')
 FROM organizing.synthesis_publication_merge_baseline WHERE workspace_id=? AND document_id=? AND article_revision_id=?`, string(document.WorkspaceID), string(document.ID), string(revision.ID))
	if err != nil {
		return nil, classifyGORM(ctx, err, "AUTHORING_MERGE_BASELINE_QUERY_FAILED")
	}
	var p publicationMergeProof
	if err = row.Scan(&p.ReceiptID, &p.CaptureID, &p.PublishedRevisionID, &p.PublishedContentHash, &p.FileBase, &p.TargetPath, &p.ContentHash, &p.DocumentVersion, &p.Valid, &p.HistoricalRepublishID); err != nil {
		if gormNoRows(err) {
			return nil, nil
		}
		return nil, classifyGORM(ctx, err, "AUTHORING_MERGE_BASELINE_QUERY_FAILED")
	}
	if !p.Valid || p.DocumentVersion != document.Version || p.TargetPath != document.CanonicalPath || p.ContentHash != revision.ContentHash || p.PublishedRevisionID != document.CurrentPublishedRevisionID {
		return nil, publicationConflict("manuscript publication baseline changed")
	}
	latest, err := gormLoadLatestRevision(ctx, tx, document.WorkspaceID, document.ID, true)
	if err != nil {
		return nil, err
	}
	if latest.ID != revision.ID {
		return nil, publicationConflict("manuscript is no longer the latest candidate")
	}
	if p.PublishedRevisionID != "" {
		published, err := gormLoadRevision(ctx, tx, document.WorkspaceID, document.ID, p.PublishedRevisionID, true)
		if err != nil {
			return nil, err
		}
		if published.Status != domain.RevisionPublished || published.ContentHash != p.PublishedContentHash || published.GitCommit == "" {
			return nil, publicationConflict("published manuscript baseline changed")
		}
	}
	return &p, nil
}

func gormValidateStoredMergeBaseline(ctx context.Context, tx *gorm.DB, reservation authoringapp.PublicationReservation, document domain.Document, revision domain.ArticleRevision) error {
	if reservation.MergeBaseline == nil {
		return nil
	}
	proof, err := gormPublicationMergeBaseline(ctx, tx, document, revision)
	if err != nil {
		return err
	}
	if proof == nil || proof.PublicationMergeBaseline != *reservation.MergeBaseline || proof.FileBase != reservation.BaseVersion {
		return inconsistent("stored publication merge proof changed")
	}
	return nil
}

func reservationReplacesRevision(r authoringapp.PublicationReservation, previous domain.ArticleRevision) bool {
	if r.MergeBaseline == nil {
		return previous.ContentHash == r.BaseVersion
	}
	return previous.ID == r.MergeBaseline.PublishedRevisionID && previous.ContentHash == r.MergeBaseline.PublishedContentHash
}

// ValidatePublicationWriteback 在取得文件锁后检查
// 当前所有者基线。普通文件提案和非手稿绑定保持
// 原有行为；文件字节的 CAS 仍由 Safe Writeback 负责。
func (repository *GORMRepository) ValidatePublicationWriteback(ctx context.Context, workspace, proposal, proposalRevision foundation.ID) error {
	return repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		binding, found, err := gormLoadPublicationProposal(ctx, tx, workspace, proposal, proposalRevision)
		if err != nil || !found {
			return err
		}
		reservation, found, err := gormLoadReservation(ctx, tx, workspace, binding.ReservationID, false)
		if err != nil {
			return err
		}
		if !found {
			return inconsistent("publication reservation is missing")
		}
		if reservation.MergeBaseline == nil {
			return nil
		}
		document, revision, err := gormLoadReservedPublicationFactsGORM(ctx, tx, authoringapp.PublishBinding{WorkspaceID: workspace, DocumentID: binding.DocumentID, RevisionID: binding.ArticleRevisionID})
		if err != nil {
			return err
		}
		proof, err := gormPublicationMergeBaseline(ctx, tx, document, revision)
		if err != nil {
			return err
		}
		if proof == nil || proof.PublicationMergeBaseline != *reservation.MergeBaseline || proof.FileBase != reservation.BaseVersion || !reservationMatchesBinding(reservation, binding) {
			return inconsistent("publication merge baseline is not exact")
		}
		return gormValidateProposalSnapshot(ctx, tx, reservation, proposal, proposalRevision, revision.Content)
	}, "AUTHORING_MERGE_WRITEBACK_CHECK_FAILED")
}
