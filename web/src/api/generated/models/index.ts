/* tslint:disable */
/* eslint-disable */
/**
 *
 * @export
 * @interface APITokenCredential
 */
export interface APITokenCredential {
    /**
     *
     * @type {string}
     * @memberof APITokenCredential
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof APITokenCredential
     */
    name: string;
    /**
     *
     * @type {Array<AuthCapability>}
     * @memberof APITokenCredential
     */
    scopes: Array<AuthCapability>;
    /**
     *
     * @type {string}
     * @memberof APITokenCredential
     */
    expires_at: string;
    /**
     * Plaintext API Token returned exactly once.
     * @type {string}
     * @memberof APITokenCredential
     */
    readonly token: string;
}
/**
 *
 * @export
 * @interface APITokenInfo
 */
export interface APITokenInfo {
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    name: string;
    /**
     *
     * @type {Array<AuthCapability>}
     * @memberof APITokenInfo
     */
    scopes: Array<AuthCapability>;
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    last_used_at?: string;
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    expires_at: string;
    /**
     *
     * @type {string}
     * @memberof APITokenInfo
     */
    revoked_at?: string;
}
/**
 *
 * @export
 * @interface APITokenPage
 */
export interface APITokenPage {
    /**
     *
     * @type {Array<APITokenInfo>}
     * @memberof APITokenPage
     */
    items: Array<APITokenInfo>;
    /**
     *
     * @type {string}
     * @memberof APITokenPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface ActiveWorkspace
 */
export interface ActiveWorkspace {
    /**
     *
     * @type {string}
     * @memberof ActiveWorkspace
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ActiveWorkspace
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof ActiveWorkspace
     */
    root_path: string;
    /**
     *
     * @type {ActiveWorkspaceStatusEnum}
     * @memberof ActiveWorkspace
     */
    status: ActiveWorkspaceStatusEnum;
    /**
     *
     * @type {ActiveWorkspaceAvailabilityEnum}
     * @memberof ActiveWorkspace
     */
    availability: ActiveWorkspaceAvailabilityEnum;
    /**
     *
     * @type {number}
     * @memberof ActiveWorkspace
     */
    version: number;
}


/**
 * @export
 */
export const ActiveWorkspaceStatusEnum = {
    Active: 'active'
} as const;
export type ActiveWorkspaceStatusEnum = typeof ActiveWorkspaceStatusEnum[keyof typeof ActiveWorkspaceStatusEnum];

/**
 * @export
 */
export const ActiveWorkspaceAvailabilityEnum = {
    Available: 'available',
    Unavailable: 'unavailable',
    MigrationRequired: 'migration_required'
} as const;
export type ActiveWorkspaceAvailabilityEnum = typeof ActiveWorkspaceAvailabilityEnum[keyof typeof ActiveWorkspaceAvailabilityEnum];

/**
 *
 * @export
 * @interface Answer
 */
export interface Answer {
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    conversation_id: string;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    question_id: string;
    /**
     *
     * @type {AnswerPublicationStatusEnum}
     * @memberof Answer
     */
    publication_status: AnswerPublicationStatusEnum;
    /**
     *
     * @type {WorkflowProjection}
     * @memberof Answer
     */
    workflow: WorkflowProjection;
    /**
     *
     * @type {AnswerResultTypeEnum}
     * @memberof Answer
     */
    result_type?: AnswerResultTypeEnum;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    assistant_text?: string;
    /**
     *
     * @type {Array<AnswerCitation>}
     * @memberof Answer
     */
    citations: Array<AnswerCitation>;
    /**
     *
     * @type {AnswerResult}
     * @memberof Answer
     */
    result?: AnswerResult;
    /**
     *
     * @type {RetrievalSummary}
     * @memberof Answer
     */
    retrieval_summary: RetrievalSummary | null;
    /**
     *
     * @type {AnswerCurrentStageEnum}
     * @memberof Answer
     */
    current_stage: AnswerCurrentStageEnum | null;
    /**
     *
     * @type {number}
     * @memberof Answer
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Answer
     */
    updated_at: string;
}


/**
 * @export
 */
export const AnswerPublicationStatusEnum = {
    Pending: 'pending',
    Completed: 'completed',
    Refused: 'refused',
    ClarificationRequired: 'clarification_required',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type AnswerPublicationStatusEnum = typeof AnswerPublicationStatusEnum[keyof typeof AnswerPublicationStatusEnum];

/**
 * @export
 */
export const AnswerResultTypeEnum = {
    RagAnswer: 'rag_answer',
    Refusal: 'refusal',
    Clarification: 'clarification',
    WorkspaceAnalysis: 'workspace_analysis',
    WorkspaceAnalysisRefusal: 'workspace_analysis_refusal',
    WorkspaceAnalysisTermination: 'workspace_analysis_termination'
} as const;
export type AnswerResultTypeEnum = typeof AnswerResultTypeEnum[keyof typeof AnswerResultTypeEnum];

/**
 * @export
 */
export const AnswerCurrentStageEnum = {
    PlanStarted: 'plan.started',
    PlanCompleted: 'plan.completed',
    RetrievalStarted: 'retrieval.started',
    RetrievalCompleted: 'retrieval.completed',
    ValidationStarted: 'validation.started',
    ValidationCompleted: 'validation.completed'
} as const;
export type AnswerCurrentStageEnum = typeof AnswerCurrentStageEnum[keyof typeof AnswerCurrentStageEnum];

/**
 *
 * @export
 * @interface AnswerCitation
 */
export interface AnswerCitation {
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    chunk_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerCitation
     */
    href: string;
}
/**
 *
 * @export
 * @interface AnswerDraftChunk
 */
export interface AnswerDraftChunk {
    /**
     *
     * @type {number}
     * @memberof AnswerDraftChunk
     */
    generation: number;
    /**
     *
     * @type {number}
     * @memberof AnswerDraftChunk
     */
    sequence: number;
    /**
     *
     * @type {string}
     * @memberof AnswerDraftChunk
     */
    content: string;
}
/**
 *
 * @export
 * @interface AnswerDraftEnd
 */
export interface AnswerDraftEnd {
    /**
     *
     * @type {number}
     * @memberof AnswerDraftEnd
     */
    generation: number;
    /**
     *
     * @type {AnswerDraftEndStatusEnum}
     * @memberof AnswerDraftEnd
     */
    status: AnswerDraftEndStatusEnum;
    /**
     *
     * @type {AnswerDraftEndActionEnum}
     * @memberof AnswerDraftEnd
     */
    action: AnswerDraftEndActionEnum;
}


/**
 * @export
 */
export const AnswerDraftEndStatusEnum = {
    Published: 'PUBLISHED',
    Reset: 'RESET'
} as const;
export type AnswerDraftEndStatusEnum = typeof AnswerDraftEndStatusEnum[keyof typeof AnswerDraftEndStatusEnum];

/**
 * @export
 */
export const AnswerDraftEndActionEnum = {
    Refetch: 'refetch'
} as const;
export type AnswerDraftEndActionEnum = typeof AnswerDraftEndActionEnum[keyof typeof AnswerDraftEndActionEnum];

/**
 *
 * @export
 * @interface AnswerDraftReset
 */
export interface AnswerDraftReset {
    /**
     *
     * @type {number}
     * @memberof AnswerDraftReset
     */
    generation: number;
    /**
     *
     * @type {AnswerDraftResetReasonEnum}
     * @memberof AnswerDraftReset
     */
    reason: AnswerDraftResetReasonEnum;
    /**
     *
     * @type {AnswerDraftResetActionEnum}
     * @memberof AnswerDraftReset
     */
    action: AnswerDraftResetActionEnum;
}


/**
 * @export
 */
export const AnswerDraftResetReasonEnum = {
    GenerationReplaced: 'generation_replaced',
    DraftUnavailable: 'draft_unavailable',
    DraftStale: 'draft_stale',
    Aborted: 'aborted',
    Superseded: 'superseded'
} as const;
export type AnswerDraftResetReasonEnum = typeof AnswerDraftResetReasonEnum[keyof typeof AnswerDraftResetReasonEnum];

/**
 * @export
 */
export const AnswerDraftResetActionEnum = {
    Refetch: 'refetch'
} as const;
export type AnswerDraftResetActionEnum = typeof AnswerDraftResetActionEnum[keyof typeof AnswerDraftResetActionEnum];

/**
 *
 * @export
 * @interface AnswerFeedback
 */
export interface AnswerFeedback {
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    answer_id: string;
    /**
     *
     * @type {AnswerFeedbackFeedbackTypeEnum}
     * @memberof AnswerFeedback
     */
    feedback_type: AnswerFeedbackFeedbackTypeEnum;
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    citation_id: string | null;
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    comment: string | null;
    /**
     *
     * @type {string}
     * @memberof AnswerFeedback
     */
    created_at: string;
}


/**
 * @export
 */
export const AnswerFeedbackFeedbackTypeEnum = {
    Helpful: 'helpful',
    Incorrect: 'incorrect',
    IrrelevantCitation: 'irrelevant_citation',
    BrokenCitation: 'broken_citation',
    MissingSource: 'missing_source'
} as const;
export type AnswerFeedbackFeedbackTypeEnum = typeof AnswerFeedbackFeedbackTypeEnum[keyof typeof AnswerFeedbackFeedbackTypeEnum];

/**
 * @type AnswerResult
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type AnswerResult = ClarificationResult | RAGAnswerResult | RefusalResult | WorkspaceAnalysisAnswerResult | WorkspaceAnalysisRefusalResult | WorkspaceAnalysisTerminationResult;

/**
 *
 * @export
 * @interface AppendProposalRevision200Response
 */
export interface AppendProposalRevision200Response {
    /**
     *
     * @type {AppendProposalRevision200ResponseProposalTypeEnum}
     * @memberof AppendProposalRevision200Response
     */
    proposal_type: AppendProposalRevision200ResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision200Response
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision200Response
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision200Response
     */
    target_path: string;
    /**
     *
     * @type {AppendProposalRevision200ResponseStatusEnum}
     * @memberof AppendProposalRevision200Response
     */
    status: AppendProposalRevision200ResponseStatusEnum;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof AppendProposalRevision200Response
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {number}
     * @memberof AppendProposalRevision200Response
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof AppendProposalRevision200Response
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {ProposalRevision}
     * @memberof AppendProposalRevision200Response
     */
    revision: ProposalRevision;
    /**
     *
     * @type {null}
     * @memberof AppendProposalRevision200Response
     */
    approval: null;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision200Response
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision200Response
     */
    updated_at: string;
    /**
     *
     * @type {AppendProposalRevision200ResponseReplayedEnum}
     * @memberof AppendProposalRevision200Response
     */
    replayed: AppendProposalRevision200ResponseReplayedEnum;
}


/**
 * @export
 */
export const AppendProposalRevision200ResponseProposalTypeEnum = {
    FilePatch: 'file_patch'
} as const;
export type AppendProposalRevision200ResponseProposalTypeEnum = typeof AppendProposalRevision200ResponseProposalTypeEnum[keyof typeof AppendProposalRevision200ResponseProposalTypeEnum];

/**
 * @export
 */
export const AppendProposalRevision200ResponseStatusEnum = {
    ReadyForReview: 'ready_for_review'
} as const;
export type AppendProposalRevision200ResponseStatusEnum = typeof AppendProposalRevision200ResponseStatusEnum[keyof typeof AppendProposalRevision200ResponseStatusEnum];

/**
 * @export
 */
export const AppendProposalRevision200ResponseReplayedEnum = {
    True: true
} as const;
export type AppendProposalRevision200ResponseReplayedEnum = typeof AppendProposalRevision200ResponseReplayedEnum[keyof typeof AppendProposalRevision200ResponseReplayedEnum];

/**
 *
 * @export
 * @interface AppendProposalRevision201Response
 */
export interface AppendProposalRevision201Response {
    /**
     *
     * @type {AppendProposalRevision201ResponseProposalTypeEnum}
     * @memberof AppendProposalRevision201Response
     */
    proposal_type: AppendProposalRevision201ResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision201Response
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision201Response
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision201Response
     */
    target_path: string;
    /**
     *
     * @type {AppendProposalRevision201ResponseStatusEnum}
     * @memberof AppendProposalRevision201Response
     */
    status: AppendProposalRevision201ResponseStatusEnum;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof AppendProposalRevision201Response
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {number}
     * @memberof AppendProposalRevision201Response
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof AppendProposalRevision201Response
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {ProposalRevision}
     * @memberof AppendProposalRevision201Response
     */
    revision: ProposalRevision;
    /**
     *
     * @type {null}
     * @memberof AppendProposalRevision201Response
     */
    approval: null;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision201Response
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevision201Response
     */
    updated_at: string;
    /**
     *
     * @type {AppendProposalRevision201ResponseReplayedEnum}
     * @memberof AppendProposalRevision201Response
     */
    replayed: AppendProposalRevision201ResponseReplayedEnum;
}


/**
 * @export
 */
export const AppendProposalRevision201ResponseProposalTypeEnum = {
    FilePatch: 'file_patch'
} as const;
export type AppendProposalRevision201ResponseProposalTypeEnum = typeof AppendProposalRevision201ResponseProposalTypeEnum[keyof typeof AppendProposalRevision201ResponseProposalTypeEnum];

/**
 * @export
 */
export const AppendProposalRevision201ResponseStatusEnum = {
    ReadyForReview: 'ready_for_review'
} as const;
export type AppendProposalRevision201ResponseStatusEnum = typeof AppendProposalRevision201ResponseStatusEnum[keyof typeof AppendProposalRevision201ResponseStatusEnum];

/**
 * @export
 */
export const AppendProposalRevision201ResponseReplayedEnum = {
    False: false
} as const;
export type AppendProposalRevision201ResponseReplayedEnum = typeof AppendProposalRevision201ResponseReplayedEnum[keyof typeof AppendProposalRevision201ResponseReplayedEnum];

/**
 *
 * @export
 * @interface AppendProposalRevisionRequest
 */
export interface AppendProposalRevisionRequest {
    /**
     *
     * @type {number}
     * @memberof AppendProposalRevisionRequest
     */
    expected_proposal_version: number;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    source_revision_id: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    source_change_hash: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    expected_current_hash: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    merge_fingerprint: string;
    /**
     *
     * @type {AppendProposalRevisionRequestMergeAlgorithmEnum}
     * @memberof AppendProposalRevisionRequest
     */
    merge_algorithm: AppendProposalRevisionRequestMergeAlgorithmEnum;
    /**
     *
     * @type {AppendProposalRevisionRequestMergeAlgorithmVersionEnum}
     * @memberof AppendProposalRevisionRequest
     */
    merge_algorithm_version: AppendProposalRevisionRequestMergeAlgorithmVersionEnum;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    evidence_summary: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof AppendProposalRevisionRequest
     */
    rollback_plan: string;
    /**
     *
     * @type {Array<string>}
     * @memberof AppendProposalRevisionRequest
     */
    resolved_conflict_ids: Array<string>;
}


/**
 * @export
 */
export const AppendProposalRevisionRequestMergeAlgorithmEnum = {
    GitMergeFile: 'git-merge-file'
} as const;
export type AppendProposalRevisionRequestMergeAlgorithmEnum = typeof AppendProposalRevisionRequestMergeAlgorithmEnum[keyof typeof AppendProposalRevisionRequestMergeAlgorithmEnum];

/**
 * @export
 */
export const AppendProposalRevisionRequestMergeAlgorithmVersionEnum = {
    Diff3MyersMarker32V1: 'diff3/myers/marker32/v1'
} as const;
export type AppendProposalRevisionRequestMergeAlgorithmVersionEnum = typeof AppendProposalRevisionRequestMergeAlgorithmVersionEnum[keyof typeof AppendProposalRevisionRequestMergeAlgorithmVersionEnum];

/**
 *
 * @export
 * @interface ApplyPreflightRequest
 */
export interface ApplyPreflightRequest {
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightRequest
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightRequest
     */
    approved_change_hash: string;
}
/**
 *
 * @export
 * @interface ApplyPreflightResult
 */
export interface ApplyPreflightResult {
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightResult
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightResult
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightResult
     */
    change_hash: string;
    /**
     *
     * @type {ApplyPreflightResultTargetModeEnum}
     * @memberof ApplyPreflightResult
     */
    target_mode: ApplyPreflightResultTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof ApplyPreflightResult
     */
    base_hash: string;
    /**
     *
     * @type {ApplyPreflightResultPreflightPassedEnum}
     * @memberof ApplyPreflightResult
     */
    preflight_passed: ApplyPreflightResultPreflightPassedEnum;
    /**
     *
     * @type {ApplyPreflightResultModeEnum}
     * @memberof ApplyPreflightResult
     */
    mode: ApplyPreflightResultModeEnum;
    /**
     *
     * @type {ApplyPreflightResultWritePerformedEnum}
     * @memberof ApplyPreflightResult
     */
    write_performed: ApplyPreflightResultWritePerformedEnum;
}


/**
 * @export
 */
export const ApplyPreflightResultTargetModeEnum = {
    Replace: 'REPLACE',
    CreateOnly: 'CREATE_ONLY'
} as const;
export type ApplyPreflightResultTargetModeEnum = typeof ApplyPreflightResultTargetModeEnum[keyof typeof ApplyPreflightResultTargetModeEnum];

/**
 * @export
 */
export const ApplyPreflightResultPreflightPassedEnum = {
    True: true
} as const;
export type ApplyPreflightResultPreflightPassedEnum = typeof ApplyPreflightResultPreflightPassedEnum[keyof typeof ApplyPreflightResultPreflightPassedEnum];

/**
 * @export
 */
export const ApplyPreflightResultModeEnum = {
    PreflightOnly: 'preflight_only'
} as const;
export type ApplyPreflightResultModeEnum = typeof ApplyPreflightResultModeEnum[keyof typeof ApplyPreflightResultModeEnum];

/**
 * @export
 */
export const ApplyPreflightResultWritePerformedEnum = {
    False: false
} as const;
export type ApplyPreflightResultWritePerformedEnum = typeof ApplyPreflightResultWritePerformedEnum[keyof typeof ApplyPreflightResultWritePerformedEnum];

/**
 * Persistent Approval snapshot returned by Proposal detail and list reads. New approved file_patch and restore_document snapshots include the durable Git and Workflow binding. Historical approved file_patch snapshots may omit workflow_run_id/workflow_status_url while awaiting an exact approval replay; snapshots that also omit approved_git_head are read-only manual-recovery records. Approved restore_document snapshots always bind approved_git_head to the verified restore HEAD. Approved non-file typed Proposal snapshots and rejected snapshots omit all Git and Workflow fields. The transient dispatch_status exists only on ApprovalDecisionResponse.
 * @export
 * @interface Approval
 */
export interface Approval {
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    change_hash: string;
    /**
     *
     * @type {ApprovalDecisionEnum}
     * @memberof Approval
     */
    decision: ApprovalDecisionEnum;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    approved_git_head?: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    decided_at: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    workflow_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof Approval
     */
    workflow_status_url?: string;
}


/**
 * @export
 */
export const ApprovalDecisionEnum = {
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type ApprovalDecisionEnum = typeof ApprovalDecisionEnum[keyof typeof ApprovalDecisionEnum];

/**
 * Approved file_patch and restore_document decisions include approved_git_head, workflow_run_id, workflow_status_url and dispatch_status. Approved non-file typed Proposal decisions and rejected decisions omit all Git and Workflow writeback fields.
 * @export
 * @interface ApprovalDecisionResponse
 */
export interface ApprovalDecisionResponse {
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    change_hash: string;
    /**
     *
     * @type {ApprovalDecisionResponseDecisionEnum}
     * @memberof ApprovalDecisionResponse
     */
    decision: ApprovalDecisionResponseDecisionEnum;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    approved_git_head?: string;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    workflow_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    workflow_status_url?: string;
    /**
     *
     * @type {ApprovalDecisionResponseDispatchStatusEnum}
     * @memberof ApprovalDecisionResponse
     */
    dispatch_status?: ApprovalDecisionResponseDispatchStatusEnum;
    /**
     *
     * @type {string}
     * @memberof ApprovalDecisionResponse
     */
    decided_at: string;
}


/**
 * @export
 */
export const ApprovalDecisionResponseDecisionEnum = {
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type ApprovalDecisionResponseDecisionEnum = typeof ApprovalDecisionResponseDecisionEnum[keyof typeof ApprovalDecisionResponseDecisionEnum];

/**
 * @export
 */
export const ApprovalDecisionResponseDispatchStatusEnum = {
    Queued: 'queued',
    Running: 'running',
    Replayed: 'replayed'
} as const;
export type ApprovalDecisionResponseDispatchStatusEnum = typeof ApprovalDecisionResponseDispatchStatusEnum[keyof typeof ApprovalDecisionResponseDispatchStatusEnum];

/**
 *
 * @export
 * @interface ArticleRevision
 */
export interface ArticleRevision {
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    document_id: string;
    /**
     *
     * @type {DocumentDraftCurrentPublishedRevisionId}
     * @memberof ArticleRevision
     */
    source_version_id: DocumentDraftCurrentPublishedRevisionId;
    /**
     *
     * @type {DocumentDraftCurrentPublishedRevisionId}
     * @memberof ArticleRevision
     */
    parent_revision_id: DocumentDraftCurrentPublishedRevisionId;
    /**
     *
     * @type {number}
     * @memberof ArticleRevision
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    content_hash: string;
    /**
     *
     * @type {ArticleRevisionStatusEnum}
     * @memberof ArticleRevision
     */
    status: ArticleRevisionStatusEnum;
    /**
     *
     * @type {ArticleRevisionOptimizationModeEnum}
     * @memberof ArticleRevision
     */
    optimization_mode: ArticleRevisionOptimizationModeEnum;
    /**
     *
     * @type {ArticleRevisionGitCommit}
     * @memberof ArticleRevision
     */
    git_commit: ArticleRevisionGitCommit;
    /**
     *
     * @type {ArticleRevisionCreatedByTypeEnum}
     * @memberof ArticleRevision
     */
    created_by_type: ArticleRevisionCreatedByTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ArticleRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const ArticleRevisionStatusEnum = {
    Draft: 'DRAFT',
    Review: 'REVIEW',
    Approved: 'APPROVED',
    Published: 'PUBLISHED',
    Superseded: 'SUPERSEDED',
    Archived: 'ARCHIVED'
} as const;
export type ArticleRevisionStatusEnum = typeof ArticleRevisionStatusEnum[keyof typeof ArticleRevisionStatusEnum];

/**
 * @export
 */
export const ArticleRevisionOptimizationModeEnum = {
    None: 'NONE',
    Clarity: 'CLARITY',
    Structure: 'STRUCTURE',
    Completeness: 'COMPLETENESS'
} as const;
export type ArticleRevisionOptimizationModeEnum = typeof ArticleRevisionOptimizationModeEnum[keyof typeof ArticleRevisionOptimizationModeEnum];

/**
 * @export
 */
export const ArticleRevisionCreatedByTypeEnum = {
    User: 'USER',
    Agent: 'AGENT',
    System: 'SYSTEM'
} as const;
export type ArticleRevisionCreatedByTypeEnum = typeof ArticleRevisionCreatedByTypeEnum[keyof typeof ArticleRevisionCreatedByTypeEnum];

/**
 * @type ArticleRevisionGitCommit
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ArticleRevisionGitCommit = null | string;

/**
 *
 * @export
 * @interface Artifact
 */
export interface Artifact {
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    type: string;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    title: string;
    /**
     *
     * @type {ArtifactStatusEnum}
     * @memberof Artifact
     */
    status: ArtifactStatusEnum;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    scope_definition: string;
    /**
     *
     * @type {Array<ArtifactCoverage>}
     * @memberof Artifact
     */
    source_coverage: Array<ArtifactCoverage>;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    current_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof Artifact
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Artifact
     */
    updated_at: string;
    /**
     *
     * @type {ArtifactRevision}
     * @memberof Artifact
     */
    revision: ArtifactRevision;
}


/**
 * @export
 */
export const ArtifactStatusEnum = {
    Planning: 'PLANNING',
    OutlineReview: 'OUTLINE_REVIEW',
    Generating: 'GENERATING',
    Draft: 'DRAFT',
    Approved: 'APPROVED',
    Exported: 'EXPORTED',
    PublishProposed: 'PUBLISH_PROPOSED',
    Published: 'PUBLISHED',
    Archived: 'ARCHIVED'
} as const;
export type ArtifactStatusEnum = typeof ArtifactStatusEnum[keyof typeof ArtifactStatusEnum];

/**
 *
 * @export
 * @interface ArtifactCitation
 */
export interface ArtifactCitation {
    /**
     *
     * @type {string}
     * @memberof ArtifactCitation
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitation
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitation
     */
    verified_content_hash: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitation
     */
    excerpt: string;
    /**
     *
     * @type {ArtifactCitationVerifiedEnum}
     * @memberof ArtifactCitation
     */
    verified: ArtifactCitationVerifiedEnum;
}


/**
 * @export
 */
export const ArtifactCitationVerifiedEnum = {
    True: true
} as const;
export type ArtifactCitationVerifiedEnum = typeof ArtifactCitationVerifiedEnum[keyof typeof ArtifactCitationVerifiedEnum];

/**
 *
 * @export
 * @interface ArtifactCitationInput
 */
export interface ArtifactCitationInput {
    /**
     *
     * @type {string}
     * @memberof ArtifactCitationInput
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitationInput
     */
    chunk_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitationInput
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactCitationInput
     */
    source_span_id: string;
}
/**
 *
 * @export
 * @interface ArtifactCommandResult
 */
export interface ArtifactCommandResult {
    /**
     *
     * @type {Artifact}
     * @memberof ArtifactCommandResult
     */
    artifact: Artifact;
    /**
     *
     * @type {ArtifactExport}
     * @memberof ArtifactCommandResult
     */
    _export?: ArtifactExport;
    /**
     *
     * @type {ArtifactPublication}
     * @memberof ArtifactCommandResult
     */
    publication?: ArtifactPublication;
    /**
     *
     * @type {boolean}
     * @memberof ArtifactCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ArtifactCoverage
 */
export interface ArtifactCoverage {
    /**
     *
     * @type {string}
     * @memberof ArtifactCoverage
     */
    section_key: string;
    /**
     *
     * @type {ArtifactCoverageStatusEnum}
     * @memberof ArtifactCoverage
     */
    status: ArtifactCoverageStatusEnum;
    /**
     *
     * @type {Array<ArtifactGap>}
     * @memberof ArtifactCoverage
     */
    gaps: Array<ArtifactGap>;
}


/**
 * @export
 */
export const ArtifactCoverageStatusEnum = {
    Covered: 'COVERED',
    Partial: 'PARTIAL',
    Gap: 'GAP'
} as const;
export type ArtifactCoverageStatusEnum = typeof ArtifactCoverageStatusEnum[keyof typeof ArtifactCoverageStatusEnum];

/**
 *
 * @export
 * @interface ArtifactDocumentSource
 */
export interface ArtifactDocumentSource {
    /**
     *
     * @type {string}
     * @memberof ArtifactDocumentSource
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactDocumentSource
     */
    article_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactDocumentSource
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactDocumentSource
     */
    verified_content_hash: string;
    /**
     *
     * @type {ArtifactDocumentSourceVerifiedEnum}
     * @memberof ArtifactDocumentSource
     */
    verified: ArtifactDocumentSourceVerifiedEnum;
}


/**
 * @export
 */
export const ArtifactDocumentSourceVerifiedEnum = {
    True: true
} as const;
export type ArtifactDocumentSourceVerifiedEnum = typeof ArtifactDocumentSourceVerifiedEnum[keyof typeof ArtifactDocumentSourceVerifiedEnum];

/**
 *
 * @export
 * @interface ArtifactEventOwnerBinding
 */
export interface ArtifactEventOwnerBinding {
    /**
     *
     * @type {ArtifactImpactBinding}
     * @memberof ArtifactEventOwnerBinding
     */
    artifact: ArtifactImpactBinding;
}
/**
 *
 * @export
 * @interface ArtifactExport
 */
export interface ArtifactExport {
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactExport
     */
    artifact_version: number;
    /**
     *
     * @type {number}
     * @memberof ArtifactExport
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    revision_hash: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    output_hash: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactExport
     */
    output_size: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactExport
     */
    exported_at: string;
}
/**
 *
 * @export
 * @interface ArtifactGap
 */
export interface ArtifactGap {
    /**
     *
     * @type {string}
     * @memberof ArtifactGap
     */
    code: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactGap
     */
    description: string;
}
/**
 *
 * @export
 * @interface ArtifactGenerationMetadata
 */
export interface ArtifactGenerationMetadata {
    /**
     *
     * @type {string}
     * @memberof ArtifactGenerationMetadata
     */
    prompt_version: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactGenerationMetadata
     */
    model_version: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactGenerationMetadata
     */
    workflow_definition_version: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactGenerationMetadata
     */
    schema_version: string;
}
/**
 *
 * @export
 * @interface ArtifactImpactBinding
 */
export interface ArtifactImpactBinding {
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactBinding
     */
    artifact_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactImpactBinding
     */
    artifact_version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactBinding
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactImpactBinding
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactBinding
     */
    content_hash: string;
}
/**
 *
 * @export
 * @interface ArtifactImpactObject
 */
export interface ArtifactImpactObject {
    /**
     *
     * @type {ArtifactImpactObjectTypeEnum}
     * @memberof ArtifactImpactObject
     */
    type: ArtifactImpactObjectTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactObject
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactObject
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactImpactObject
     */
    version: number;
    /**
     *
     * @type {ArtifactImpactObjectActionEnum}
     * @memberof ArtifactImpactObject
     */
    action: ArtifactImpactObjectActionEnum;
    /**
     *
     * @type {string}
     * @memberof ArtifactImpactObject
     */
    reason: string;
    /**
     *
     * @type {ArtifactImpactObjectRequiresProposalEnum}
     * @memberof ArtifactImpactObject
     */
    requires_proposal: ArtifactImpactObjectRequiresProposalEnum;
    /**
     *
     * @type {ArtifactImpactBinding}
     * @memberof ArtifactImpactObject
     */
    artifact_binding: ArtifactImpactBinding;
}


/**
 * @export
 */
export const ArtifactImpactObjectTypeEnum = {
    Artifact: 'ARTIFACT'
} as const;
export type ArtifactImpactObjectTypeEnum = typeof ArtifactImpactObjectTypeEnum[keyof typeof ArtifactImpactObjectTypeEnum];

/**
 * @export
 */
export const ArtifactImpactObjectActionEnum = {
    RegenerateArtifact: 'REGENERATE_ARTIFACT'
} as const;
export type ArtifactImpactObjectActionEnum = typeof ArtifactImpactObjectActionEnum[keyof typeof ArtifactImpactObjectActionEnum];

/**
 * @export
 */
export const ArtifactImpactObjectRequiresProposalEnum = {
    True: true
} as const;
export type ArtifactImpactObjectRequiresProposalEnum = typeof ArtifactImpactObjectRequiresProposalEnum[keyof typeof ArtifactImpactObjectRequiresProposalEnum];

/**
 *
 * @export
 * @interface ArtifactOutlineRequest
 */
export interface ArtifactOutlineRequest {
    /**
     *
     * @type {string}
     * @memberof ArtifactOutlineRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactOutlineRequest
     */
    expected_version: number;
    /**
     *
     * @type {Array<ArtifactOutlineSection>}
     * @memberof ArtifactOutlineRequest
     */
    outline: Array<ArtifactOutlineSection>;
}
/**
 *
 * @export
 * @interface ArtifactOutlineSection
 */
export interface ArtifactOutlineSection {
    /**
     *
     * @type {string}
     * @memberof ArtifactOutlineSection
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactOutlineSection
     */
    title: string;
}
/**
 *
 * @export
 * @interface ArtifactPage
 */
export interface ArtifactPage {
    /**
     *
     * @type {string}
     * @memberof ArtifactPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<Artifact>}
     * @memberof ArtifactPage
     */
    items: Array<Artifact>;
    /**
     *
     * @type {string}
     * @memberof ArtifactPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface ArtifactPlanRequest
 */
export interface ArtifactPlanRequest {
    /**
     *
     * @type {string}
     * @memberof ArtifactPlanRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPlanRequest
     */
    type: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPlanRequest
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPlanRequest
     */
    scope_definition: string;
}
/**
 * Frozen PUBLISH_ARTIFACT Proposal binding. Formal knowledge remains unchanged until Change Control approval and execution.
 * @export
 * @interface ArtifactPublication
 */
export interface ArtifactPublication {
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactPublication
     */
    artifact_version: number;
    /**
     *
     * @type {number}
     * @memberof ArtifactPublication
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactPublication
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface ArtifactRecordSectionRequest
 */
export interface ArtifactRecordSectionRequest {
    /**
     *
     * @type {string}
     * @memberof ArtifactRecordSectionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactRecordSectionRequest
     */
    expected_version: number;
    /**
     *
     * @type {ArtifactSectionInput}
     * @memberof ArtifactRecordSectionRequest
     */
    section: ArtifactSectionInput;
}
/**
 *
 * @export
 * @interface ArtifactRevision
 */
export interface ArtifactRevision {
    /**
     *
     * @type {string}
     * @memberof ArtifactRevision
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactRevision
     */
    artifact_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactRevision
     */
    revision_no: number;
    /**
     *
     * @type {Array<ArtifactOutlineSection>}
     * @memberof ArtifactRevision
     */
    outline: Array<ArtifactOutlineSection>;
    /**
     *
     * @type {Array<ArtifactSection>}
     * @memberof ArtifactRevision
     */
    sections: Array<ArtifactSection>;
    /**
     *
     * @type {ArtifactRevisionCreatedByEnum}
     * @memberof ArtifactRevision
     */
    created_by: ArtifactRevisionCreatedByEnum;
    /**
     *
     * @type {ArtifactGenerationMetadata}
     * @memberof ArtifactRevision
     */
    metadata?: ArtifactGenerationMetadata;
    /**
     *
     * @type {string}
     * @memberof ArtifactRevision
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const ArtifactRevisionCreatedByEnum = {
    Human: 'HUMAN',
    Agent: 'AGENT'
} as const;
export type ArtifactRevisionCreatedByEnum = typeof ArtifactRevisionCreatedByEnum[keyof typeof ArtifactRevisionCreatedByEnum];

/**
 *
 * @export
 * @interface ArtifactRevisionRequest
 */
export interface ArtifactRevisionRequest {
    /**
     *
     * @type {string}
     * @memberof ArtifactRevisionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactRevisionRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface ArtifactSection
 */
export interface ArtifactSection {
    /**
     *
     * @type {string}
     * @memberof ArtifactSection
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSection
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSection
     */
    content: string;
    /**
     *
     * @type {Array<ArtifactCitation>}
     * @memberof ArtifactSection
     */
    citations: Array<ArtifactCitation>;
    /**
     *
     * @type {Array<ArtifactDocumentSource>}
     * @memberof ArtifactSection
     */
    document_sources: Array<ArtifactDocumentSource>;
    /**
     *
     * @type {ArtifactCoverage}
     * @memberof ArtifactSection
     */
    coverage: ArtifactCoverage;
}
/**
 *
 * @export
 * @interface ArtifactSectionGenerationAcceptance
 */
export interface ArtifactSectionGenerationAcceptance {
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    generation_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    source_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    source_revision_no: number;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    source_artifact_version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    section_key: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    workflow_run_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    node_run_id: string;
    /**
     *
     * @type {ArtifactSectionGenerationAcceptanceStatusEnum}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    status: ArtifactSectionGenerationAcceptanceStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    updated_at: string;
    /**
     *
     * @type {boolean}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    replayed: boolean;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationAcceptance
     */
    status_url: string;
}


/**
 * @export
 */
export const ArtifactSectionGenerationAcceptanceStatusEnum = {
    Pending: 'PENDING',
    Completed: 'COMPLETED',
    Failed: 'FAILED',
    Cancelled: 'CANCELLED',
    RecoveryRequired: 'RECOVERY_REQUIRED'
} as const;
export type ArtifactSectionGenerationAcceptanceStatusEnum = typeof ArtifactSectionGenerationAcceptanceStatusEnum[keyof typeof ArtifactSectionGenerationAcceptanceStatusEnum];

/**
 *
 * @export
 * @interface ArtifactSectionGenerationReadItem
 */
export interface ArtifactSectionGenerationReadItem {
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    generation_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    source_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationReadItem
     */
    source_revision_no: number;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationReadItem
     */
    source_artifact_version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    section_key: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    workflow_run_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    node_run_id: string;
    /**
     *
     * @type {ArtifactSectionGenerationReadItemStatusEnum}
     * @memberof ArtifactSectionGenerationReadItem
     */
    status: ArtifactSectionGenerationReadItemStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationReadItem
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadItem
     */
    status_url: string;
}


/**
 * @export
 */
export const ArtifactSectionGenerationReadItemStatusEnum = {
    Pending: 'PENDING',
    Failed: 'FAILED',
    Cancelled: 'CANCELLED',
    RecoveryRequired: 'RECOVERY_REQUIRED'
} as const;
export type ArtifactSectionGenerationReadItemStatusEnum = typeof ArtifactSectionGenerationReadItemStatusEnum[keyof typeof ArtifactSectionGenerationReadItemStatusEnum];

/**
 *
 * @export
 * @interface ArtifactSectionGenerationReadResponse
 */
export interface ArtifactSectionGenerationReadResponse {
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadResponse
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationReadResponse
     */
    artifact_id: string;
    /**
     *
     * @type {Array<ArtifactSectionGenerationReadItem>}
     * @memberof ArtifactSectionGenerationReadResponse
     */
    items: Array<ArtifactSectionGenerationReadItem>;
}
/**
 *
 * @export
 * @interface ArtifactSectionGenerationRequest
 */
export interface ArtifactSectionGenerationRequest {
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ArtifactSectionGenerationRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionGenerationRequest
     */
    section_key: string;
}
/**
 *
 * @export
 * @interface ArtifactSectionInput
 */
export interface ArtifactSectionInput {
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionInput
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionInput
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof ArtifactSectionInput
     */
    content: string;
    /**
     *
     * @type {Array<ArtifactCitationInput>}
     * @memberof ArtifactSectionInput
     */
    citations: Array<ArtifactCitationInput>;
    /**
     *
     * @type {ArtifactCoverage}
     * @memberof ArtifactSectionInput
     */
    coverage: ArtifactCoverage;
}
/**
 *
 * @export
 * @interface AttachmentExportCreateRequest
 */
export interface AttachmentExportCreateRequest {
    /**
     *
     * @type {AttachmentExportCreateRequestKindEnum}
     * @memberof AttachmentExportCreateRequest
     */
    kind: AttachmentExportCreateRequestKindEnum;
    /**
     *
     * @type {AttachmentExportCreateRequestSchemaVersionEnum}
     * @memberof AttachmentExportCreateRequest
     */
    schema_version: AttachmentExportCreateRequestSchemaVersionEnum;
    /**
     *
     * @type {AttachmentExportCreateRequestAttachmentRootContractVersionEnum}
     * @memberof AttachmentExportCreateRequest
     */
    attachment_root_contract_version: AttachmentExportCreateRequestAttachmentRootContractVersionEnum;
    /**
     *
     * @type {AttachmentExportCreateRequestContentPolicyEnum}
     * @memberof AttachmentExportCreateRequest
     */
    content_policy: AttachmentExportCreateRequestContentPolicyEnum;
    /**
     *
     * @type {number}
     * @memberof AttachmentExportCreateRequest
     */
    expires_in_seconds?: number;
}


/**
 * @export
 */
export const AttachmentExportCreateRequestKindEnum = {
    AttachmentsZip: 'ATTACHMENTS_ZIP'
} as const;
export type AttachmentExportCreateRequestKindEnum = typeof AttachmentExportCreateRequestKindEnum[keyof typeof AttachmentExportCreateRequestKindEnum];

/**
 * @export
 */
export const AttachmentExportCreateRequestSchemaVersionEnum = {
    AttachmentExportV1: 'attachment-export/v1'
} as const;
export type AttachmentExportCreateRequestSchemaVersionEnum = typeof AttachmentExportCreateRequestSchemaVersionEnum[keyof typeof AttachmentExportCreateRequestSchemaVersionEnum];

/**
 * @export
 */
export const AttachmentExportCreateRequestAttachmentRootContractVersionEnum = {
    WorkspaceAttachmentsV1: 'workspace-attachments/v1'
} as const;
export type AttachmentExportCreateRequestAttachmentRootContractVersionEnum = typeof AttachmentExportCreateRequestAttachmentRootContractVersionEnum[keyof typeof AttachmentExportCreateRequestAttachmentRootContractVersionEnum];

/**
 * @export
 */
export const AttachmentExportCreateRequestContentPolicyEnum = {
    RawUserOwned: 'RAW_USER_OWNED'
} as const;
export type AttachmentExportCreateRequestContentPolicyEnum = typeof AttachmentExportCreateRequestContentPolicyEnum[keyof typeof AttachmentExportCreateRequestContentPolicyEnum];

/**
 *
 * @export
 * @interface AttachmentExportCreateResponse
 */
export interface AttachmentExportCreateResponse {
    /**
     *
     * @type {AttachmentExportJob}
     * @memberof AttachmentExportCreateResponse
     */
    job: AttachmentExportJob;
    /**
     *
     * @type {boolean}
     * @memberof AttachmentExportCreateResponse
     */
    replayed: boolean;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof AttachmentExportCreateResponse
     */
    dispatch_pending: boolean;
}
/**
 * @type AttachmentExportJob
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type AttachmentExportJob = AttachmentExportJobOneOf | AttachmentExportJobOneOf1 | AttachmentExportJobOneOf2 | AttachmentExportJobOneOf3 | AttachmentExportJobOneOf4 | AttachmentExportJobOneOf5;

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf
 */
export interface AttachmentExportJobOneOf {
    /**
     *
     * @type {AttachmentExportJobOneOfStatusEnum}
     * @memberof AttachmentExportJobOneOf
     */
    status: AttachmentExportJobOneOfStatusEnum;
    /**
     *
     * @type {AttachmentExportJobOneOfDownloadCountEnum}
     * @memberof AttachmentExportJobOneOf
     */
    download_count?: AttachmentExportJobOneOfDownloadCountEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOfStatusEnum = {
    Pending: 'PENDING'
} as const;
export type AttachmentExportJobOneOfStatusEnum = typeof AttachmentExportJobOneOfStatusEnum[keyof typeof AttachmentExportJobOneOfStatusEnum];

/**
 * @export
 */
export const AttachmentExportJobOneOfDownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type AttachmentExportJobOneOfDownloadCountEnum = typeof AttachmentExportJobOneOfDownloadCountEnum[keyof typeof AttachmentExportJobOneOfDownloadCountEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf1
 */
export interface AttachmentExportJobOneOf1 {
    /**
     *
     * @type {AttachmentExportJobOneOf1StatusEnum}
     * @memberof AttachmentExportJobOneOf1
     */
    status: AttachmentExportJobOneOf1StatusEnum;
    /**
     *
     * @type {AttachmentExportJobOneOf1DownloadCountEnum}
     * @memberof AttachmentExportJobOneOf1
     */
    download_count?: AttachmentExportJobOneOf1DownloadCountEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOf1StatusEnum = {
    Running: 'RUNNING'
} as const;
export type AttachmentExportJobOneOf1StatusEnum = typeof AttachmentExportJobOneOf1StatusEnum[keyof typeof AttachmentExportJobOneOf1StatusEnum];

/**
 * @export
 */
export const AttachmentExportJobOneOf1DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type AttachmentExportJobOneOf1DownloadCountEnum = typeof AttachmentExportJobOneOf1DownloadCountEnum[keyof typeof AttachmentExportJobOneOf1DownloadCountEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf2
 */
export interface AttachmentExportJobOneOf2 {
    /**
     *
     * @type {AttachmentExportJobOneOf2StatusEnum}
     * @memberof AttachmentExportJobOneOf2
     */
    status: AttachmentExportJobOneOf2StatusEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOf2StatusEnum = {
    Succeeded: 'SUCCEEDED'
} as const;
export type AttachmentExportJobOneOf2StatusEnum = typeof AttachmentExportJobOneOf2StatusEnum[keyof typeof AttachmentExportJobOneOf2StatusEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf3
 */
export interface AttachmentExportJobOneOf3 {
    /**
     *
     * @type {AttachmentExportJobOneOf3StatusEnum}
     * @memberof AttachmentExportJobOneOf3
     */
    status: AttachmentExportJobOneOf3StatusEnum;
    /**
     *
     * @type {AttachmentExportJobOneOf3DownloadCountEnum}
     * @memberof AttachmentExportJobOneOf3
     */
    download_count?: AttachmentExportJobOneOf3DownloadCountEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOf3StatusEnum = {
    Failed: 'FAILED'
} as const;
export type AttachmentExportJobOneOf3StatusEnum = typeof AttachmentExportJobOneOf3StatusEnum[keyof typeof AttachmentExportJobOneOf3StatusEnum];

/**
 * @export
 */
export const AttachmentExportJobOneOf3DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type AttachmentExportJobOneOf3DownloadCountEnum = typeof AttachmentExportJobOneOf3DownloadCountEnum[keyof typeof AttachmentExportJobOneOf3DownloadCountEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf4
 */
export interface AttachmentExportJobOneOf4 {
    /**
     *
     * @type {AttachmentExportJobOneOf4StatusEnum}
     * @memberof AttachmentExportJobOneOf4
     */
    status: AttachmentExportJobOneOf4StatusEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOf4StatusEnum = {
    Expired: 'EXPIRED'
} as const;
export type AttachmentExportJobOneOf4StatusEnum = typeof AttachmentExportJobOneOf4StatusEnum[keyof typeof AttachmentExportJobOneOf4StatusEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOf5
 */
export interface AttachmentExportJobOneOf5 {
    /**
     *
     * @type {AttachmentExportJobOneOf5StatusEnum}
     * @memberof AttachmentExportJobOneOf5
     */
    status: AttachmentExportJobOneOf5StatusEnum;
    /**
     *
     * @type {AttachmentExportJobOneOf5DownloadCountEnum}
     * @memberof AttachmentExportJobOneOf5
     */
    download_count?: AttachmentExportJobOneOf5DownloadCountEnum;
}


/**
 * @export
 */
export const AttachmentExportJobOneOf5StatusEnum = {
    Cancelled: 'CANCELLED'
} as const;
export type AttachmentExportJobOneOf5StatusEnum = typeof AttachmentExportJobOneOf5StatusEnum[keyof typeof AttachmentExportJobOneOf5StatusEnum];

/**
 * @export
 */
export const AttachmentExportJobOneOf5DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type AttachmentExportJobOneOf5DownloadCountEnum = typeof AttachmentExportJobOneOf5DownloadCountEnum[keyof typeof AttachmentExportJobOneOf5DownloadCountEnum];

/**
 *
 * @export
 * @interface AttachmentExportJobOneOfNot
 */
export interface AttachmentExportJobOneOfNot {
}
/**
 *
 * @export
 * @interface AttachmentExportPage
 */
export interface AttachmentExportPage {
    /**
     *
     * @type {string}
     * @memberof AttachmentExportPage
     */
    workspace_id: string;
    /**
     *
     * @type {AttachmentExportPageScopeKindEnum}
     * @memberof AttachmentExportPage
     */
    scope_kind: AttachmentExportPageScopeKindEnum;
    /**
     *
     * @type {Array<AttachmentExportJob>}
     * @memberof AttachmentExportPage
     */
    items: Array<AttachmentExportJob>;
    /**
     *
     * @type {string}
     * @memberof AttachmentExportPage
     */
    next_cursor?: string;
}


/**
 * @export
 */
export const AttachmentExportPageScopeKindEnum = {
    WorkspaceAttachments: 'WORKSPACE_ATTACHMENTS'
} as const;
export type AttachmentExportPageScopeKindEnum = typeof AttachmentExportPageScopeKindEnum[keyof typeof AttachmentExportPageScopeKindEnum];


/**
 *
 * @export
 */
export const AuthCapability = {
    ReadLocal: 'READ_LOCAL',
    ReadExternal: 'READ_EXTERNAL',
    WriteProposal: 'WRITE_PROPOSAL',
    WriteKnowledge: 'WRITE_KNOWLEDGE',
    GitWrite: 'GIT_WRITE',
    IndexMaintenance: 'INDEX_MAINTENANCE',
    EvaluationRun: 'EVALUATION_RUN',
    ManageSystemSettings: 'MANAGE_SYSTEM_SETTINGS'
} as const;
export type AuthCapability = typeof AuthCapability[keyof typeof AuthCapability];

/**
 *
 * @export
 * @interface AuthCapabilityStatus
 */
export interface AuthCapabilityStatus {
    /**
     *
     * @type {AuthCapabilityStatusStatusEnum}
     * @memberof AuthCapabilityStatus
     */
    status: AuthCapabilityStatusStatusEnum;
    /**
     *
     * @type {AuthCapabilityStatusReasonEnum}
     * @memberof AuthCapabilityStatus
     */
    reason?: AuthCapabilityStatusReasonEnum;
}


/**
 * @export
 */
export const AuthCapabilityStatusStatusEnum = {
    Disabled: 'disabled',
    Ready: 'ready',
    Unavailable: 'unavailable'
} as const;
export type AuthCapabilityStatusStatusEnum = typeof AuthCapabilityStatusStatusEnum[keyof typeof AuthCapabilityStatusStatusEnum];

/**
 * @export
 */
export const AuthCapabilityStatusReasonEnum = {
    AuthDependenciesUnavailable: 'auth_dependencies_unavailable'
} as const;
export type AuthCapabilityStatusReasonEnum = typeof AuthCapabilityStatusReasonEnum[keyof typeof AuthCapabilityStatusReasonEnum];

/**
 *
 * @export
 * @interface AuthoringOverview
 */
export interface AuthoringOverview {
    /**
     *
     * @type {string}
     * @memberof AuthoringOverview
     */
    workspace_id: string;
    /**
     *
     * @type {OrganizingAvailability}
     * @memberof AuthoringOverview
     */
    organizing: OrganizingAvailability;
    /**
     *
     * @type {Array<WorkingDraftSummary>}
     * @memberof AuthoringOverview
     */
    recent_drafts: Array<WorkingDraftSummary>;
    /**
     *
     * @type {Array<PublicationBinding>}
     * @memberof AuthoringOverview
     */
    pending_publications: Array<PublicationBinding>;
    /**
     *
     * @type {Array<DocumentDraft>}
     * @memberof AuthoringOverview
     */
    completed_documents: Array<DocumentDraft>;
}
/**
 * @type Capture
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type Capture = CaptureOneOf | CaptureOneOf1;

/**
 *
 * @export
 * @interface CaptureCommandResult
 */
export interface CaptureCommandResult {
    /**
     *
     * @type {Capture}
     * @memberof CaptureCommandResult
     */
    capture: Capture;
    /**
     *
     * @type {boolean}
     * @memberof CaptureCommandResult
     */
    replayed: boolean;
}

/**
 *
 * @export
 */
export const CaptureKind = {
    Text: 'TEXT',
    Url: 'URL',
    File: 'FILE',
    Image: 'IMAGE'
} as const;
export type CaptureKind = typeof CaptureKind[keyof typeof CaptureKind];

/**
 *
 * @export
 * @interface CaptureOneOf
 */
export interface CaptureOneOf {
    /**
     *
     * @type {CaptureOneOfKindEnum}
     * @memberof CaptureOneOf
     */
    kind?: CaptureOneOfKindEnum;
}


/**
 * @export
 */
export const CaptureOneOfKindEnum = {
    Url: 'URL'
} as const;
export type CaptureOneOfKindEnum = typeof CaptureOneOfKindEnum[keyof typeof CaptureOneOfKindEnum];

/**
 *
 * @export
 * @interface CaptureOneOf1
 */
export interface CaptureOneOf1 {
    /**
     *
     * @type {CaptureOneOf1KindEnum}
     * @memberof CaptureOneOf1
     */
    kind?: CaptureOneOf1KindEnum;
}


/**
 * @export
 */
export const CaptureOneOf1KindEnum = {
    Text: 'TEXT',
    File: 'FILE',
    Image: 'IMAGE'
} as const;
export type CaptureOneOf1KindEnum = typeof CaptureOneOf1KindEnum[keyof typeof CaptureOneOf1KindEnum];

/**
 *
 * @export
 * @interface CapturePage
 */
export interface CapturePage {
    /**
     *
     * @type {string}
     * @memberof CapturePage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<Capture>}
     * @memberof CapturePage
     */
    items: Array<Capture>;
    /**
     *
     * @type {string}
     * @memberof CapturePage
     */
    next_cursor?: string;
}

/**
 *
 * @export
 */
export const CaptureStageStatus = {
    Pending: 'PENDING',
    Running: 'RUNNING',
    Ready: 'READY',
    Failed: 'FAILED',
    CapabilityUnavailable: 'CAPABILITY_UNAVAILABLE',
    Stale: 'STALE',
    NotApplicable: 'NOT_APPLICABLE'
} as const;
export type CaptureStageStatus = typeof CaptureStageStatus[keyof typeof CaptureStageStatus];


/**
 *
 * @export
 */
export const CaptureStatus = {
    Received: 'RECEIVED',
    SourceSaved: 'SOURCE_SAVED',
    Fetching: 'FETCHING',
    Processing: 'PROCESSING',
    Ready: 'READY',
    ReadyDegraded: 'READY_DEGRADED',
    FetchFailed: 'FETCH_FAILED',
    ProcessingFailed: 'PROCESSING_FAILED'
} as const;
export type CaptureStatus = typeof CaptureStatus[keyof typeof CaptureStatus];

/**
 *
 * @export
 * @interface ChatConnectionTest
 */
export interface ChatConnectionTest {
    /**
     *
     * @type {ChatConnectionTestTargetEnum}
     * @memberof ChatConnectionTest
     */
    target?: ChatConnectionTestTargetEnum;
    /**
     *
     * @type {ChatConnectionTestChat}
     * @memberof ChatConnectionTest
     */
    chat: ChatConnectionTestChat;
}


/**
 * @export
 */
export const ChatConnectionTestTargetEnum = {
    Chat: 'chat'
} as const;
export type ChatConnectionTestTargetEnum = typeof ChatConnectionTestTargetEnum[keyof typeof ChatConnectionTestTargetEnum];

/**
 *
 * @export
 * @interface ChatConnectionTestChat
 */
export interface ChatConnectionTestChat {
    /**
     *
     * @type {ChatConnectionTestChatProviderEnum}
     * @memberof ChatConnectionTestChat
     */
    provider?: ChatConnectionTestChatProviderEnum;
}


/**
 * @export
 */
export const ChatConnectionTestChatProviderEnum = {
    OpenaiCompatible: 'openai-compatible',
    Ollama: 'ollama'
} as const;
export type ChatConnectionTestChatProviderEnum = typeof ChatConnectionTestChatProviderEnum[keyof typeof ChatConnectionTestChatProviderEnum];

/**
 *
 * @export
 * @interface ClarificationResult
 */
export interface ClarificationResult {
    /**
     *
     * @type {ClarificationResultResultTypeEnum}
     * @memberof ClarificationResult
     */
    result_type: ClarificationResultResultTypeEnum;
    /**
     *
     * @type {ClarificationResultSchemaIdEnum}
     * @memberof ClarificationResult
     */
    schema_id: ClarificationResultSchemaIdEnum;
    /**
     *
     * @type {ClarificationResultSchemaVersionEnum}
     * @memberof ClarificationResult
     */
    schema_version: ClarificationResultSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ClarificationResult
     */
    model_run_ref: string;
    /**
     *
     * @type {ClarificationResultPayload}
     * @memberof ClarificationResult
     */
    payload: ClarificationResultPayload;
}


/**
 * @export
 */
export const ClarificationResultResultTypeEnum = {
    Clarification: 'clarification'
} as const;
export type ClarificationResultResultTypeEnum = typeof ClarificationResultResultTypeEnum[keyof typeof ClarificationResultResultTypeEnum];

/**
 * @export
 */
export const ClarificationResultSchemaIdEnum = {
    ConversationClarification: 'conversation.clarification'
} as const;
export type ClarificationResultSchemaIdEnum = typeof ClarificationResultSchemaIdEnum[keyof typeof ClarificationResultSchemaIdEnum];

/**
 * @export
 */
export const ClarificationResultSchemaVersionEnum = {
    V1: 'v1'
} as const;
export type ClarificationResultSchemaVersionEnum = typeof ClarificationResultSchemaVersionEnum[keyof typeof ClarificationResultSchemaVersionEnum];

/**
 *
 * @export
 * @interface ClarificationResultPayload
 */
export interface ClarificationResultPayload {
    /**
     *
     * @type {string}
     * @memberof ClarificationResultPayload
     */
    reason: string;
    /**
     *
     * @type {string}
     * @memberof ClarificationResultPayload
     */
    question: string;
    /**
     *
     * @type {Array<string>}
     * @memberof ClarificationResultPayload
     */
    suggested_scopes: Array<string>;
}
/**
 *
 * @export
 * @interface Collection
 */
export interface Collection {
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    description: string;
    /**
     *
     * @type {CollectionQuerySchemaVersionEnum}
     * @memberof Collection
     */
    query_schema_version: CollectionQuerySchemaVersionEnum;
    /**
     *
     * @type {number}
     * @memberof Collection
     */
    query_version: number;
    /**
     *
     * @type {object}
     * @memberof Collection
     */
    query: object;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    query_hash: string;
    /**
     *
     * @type {CollectionViewTypeEnum}
     * @memberof Collection
     */
    view_type: CollectionViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof Collection
     */
    view_config: object;
    /**
     *
     * @type {CollectionStatusEnum}
     * @memberof Collection
     */
    status: CollectionStatusEnum;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    cached_result_version?: string;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    last_executed_at?: string;
    /**
     *
     * @type {number}
     * @memberof Collection
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Collection
     */
    updated_at: string;
}


/**
 * @export
 */
export const CollectionQuerySchemaVersionEnum = {
    CollectionQueryV1: 'collection-query/v1'
} as const;
export type CollectionQuerySchemaVersionEnum = typeof CollectionQuerySchemaVersionEnum[keyof typeof CollectionQuerySchemaVersionEnum];

/**
 * @export
 */
export const CollectionViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionViewTypeEnum = typeof CollectionViewTypeEnum[keyof typeof CollectionViewTypeEnum];

/**
 * @export
 */
export const CollectionStatusEnum = {
    Active: 'ACTIVE',
    Archived: 'ARCHIVED'
} as const;
export type CollectionStatusEnum = typeof CollectionStatusEnum[keyof typeof CollectionStatusEnum];

/**
 *
 * @export
 * @interface CollectionArchiveRequest
 */
export interface CollectionArchiveRequest {
    /**
     *
     * @type {string}
     * @memberof CollectionArchiveRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof CollectionArchiveRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface CollectionDefinitionRequest
 */
export interface CollectionDefinitionRequest {
    /**
     *
     * @type {string}
     * @memberof CollectionDefinitionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionDefinitionRequest
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof CollectionDefinitionRequest
     */
    description?: string;
    /**
     *
     * @type {object}
     * @memberof CollectionDefinitionRequest
     */
    query: object;
    /**
     *
     * @type {CollectionDefinitionRequestViewTypeEnum}
     * @memberof CollectionDefinitionRequest
     */
    view_type: CollectionDefinitionRequestViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionDefinitionRequest
     */
    view_config: object;
}


/**
 * @export
 */
export const CollectionDefinitionRequestViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionDefinitionRequestViewTypeEnum = typeof CollectionDefinitionRequestViewTypeEnum[keyof typeof CollectionDefinitionRequestViewTypeEnum];

/**
 *
 * @export
 * @interface CollectionHealthSummary
 */
export interface CollectionHealthSummary {
    /**
     *
     * @type {number}
     * @memberof CollectionHealthSummary
     */
    count: number;
    /**
     *
     * @type {CollectionHealthSummaryMaxSeverityEnum}
     * @memberof CollectionHealthSummary
     */
    max_severity: CollectionHealthSummaryMaxSeverityEnum;
    /**
     *
     * @type {Array<HealthIssueType>}
     * @memberof CollectionHealthSummary
     */
    issue_types?: Array<HealthIssueType>;
    /**
     *
     * @type {string}
     * @memberof CollectionHealthSummary
     */
    summary?: string;
}


/**
 * @export
 */
export const CollectionHealthSummaryMaxSeverityEnum = {
    Critical: 'CRITICAL',
    High: 'HIGH',
    Medium: 'MEDIUM',
    Low: 'LOW'
} as const;
export type CollectionHealthSummaryMaxSeverityEnum = typeof CollectionHealthSummaryMaxSeverityEnum[keyof typeof CollectionHealthSummaryMaxSeverityEnum];

/**
 *
 * @export
 * @interface CollectionItem
 */
export interface CollectionItem {
    /**
     *
     * @type {CollectionItemObjectTypeEnum}
     * @memberof CollectionItem
     */
    object_type: CollectionItemObjectTypeEnum;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    topic_id?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    summary: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    status: string;
    /**
     *
     * @type {number}
     * @memberof CollectionItem
     */
    confidence?: number;
    /**
     *
     * @type {object}
     * @memberof CollectionItem
     */
    applicability?: object | null;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    applicability_schema_version?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    applicability_hash?: string;
    /**
     *
     * @type {Array<string>}
     * @memberof CollectionItem
     */
    aliases?: Array<string>;
    /**
     *
     * @type {Array<CollectionSourceSummary>}
     * @memberof CollectionItem
     */
    source_summaries?: Array<CollectionSourceSummary>;
    /**
     *
     * @type {Array<string>}
     * @memberof CollectionItem
     */
    relation_types?: Array<string>;
    /**
     *
     * @type {CollectionHealthSummary}
     * @memberof CollectionItem
     */
    health_summary?: CollectionHealthSummary | null;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    relation_type?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    health_issue_type?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    source_type?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    file_path?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof CollectionItem
     */
    updated_at: string;
}


/**
 * @export
 */
export const CollectionItemObjectTypeEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM'
} as const;
export type CollectionItemObjectTypeEnum = typeof CollectionItemObjectTypeEnum[keyof typeof CollectionItemObjectTypeEnum];

/**
 *
 * @export
 * @interface CollectionList
 */
export interface CollectionList {
    /**
     *
     * @type {string}
     * @memberof CollectionList
     */
    workspace_id: string;
    /**
     *
     * @type {Array<Collection>}
     * @memberof CollectionList
     */
    items: Array<Collection>;
    /**
     *
     * @type {string}
     * @memberof CollectionList
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface CollectionPreviewRequest
 */
export interface CollectionPreviewRequest {
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewRequest
     */
    workspace_id: string;
    /**
     *
     * @type {object}
     * @memberof CollectionPreviewRequest
     */
    query: object;
    /**
     *
     * @type {CollectionPreviewRequestViewTypeEnum}
     * @memberof CollectionPreviewRequest
     */
    view_type: CollectionPreviewRequestViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionPreviewRequest
     */
    view_config: object;
    /**
     *
     * @type {number}
     * @memberof CollectionPreviewRequest
     */
    limit?: number;
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewRequest
     */
    cursor?: string;
}


/**
 * @export
 */
export const CollectionPreviewRequestViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionPreviewRequestViewTypeEnum = typeof CollectionPreviewRequestViewTypeEnum[keyof typeof CollectionPreviewRequestViewTypeEnum];

/**
 *
 * @export
 * @interface CollectionPreviewResponse
 */
export interface CollectionPreviewResponse {
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewResponse
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewResponse
     */
    query_hash: string;
    /**
     *
     * @type {Array<CollectionItem>}
     * @memberof CollectionPreviewResponse
     */
    items: Array<CollectionItem>;
    /**
     *
     * @type {number}
     * @memberof CollectionPreviewResponse
     */
    exact_count: number;
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewResponse
     */
    next_cursor?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewResponse
     */
    revision_hash: string;
    /**
     *
     * @type {string}
     * @memberof CollectionPreviewResponse
     */
    scan_revision_hash: string;
}
/**
 *
 * @export
 * @interface CollectionResultPage
 */
export interface CollectionResultPage {
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    collection_id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    query_hash: string;
    /**
     *
     * @type {Array<CollectionItem>}
     * @memberof CollectionResultPage
     */
    items: Array<CollectionItem>;
    /**
     *
     * @type {number}
     * @memberof CollectionResultPage
     */
    exact_count: number;
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    next_cursor?: string;
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    revision_hash: string;
    /**
     *
     * @type {string}
     * @memberof CollectionResultPage
     */
    scan_revision_hash: string;
}
/**
 *
 * @export
 * @interface CollectionSourceSummary
 */
export interface CollectionSourceSummary {
    /**
     *
     * @type {string}
     * @memberof CollectionSourceSummary
     */
    source_type: string;
    /**
     *
     * @type {string}
     * @memberof CollectionSourceSummary
     */
    file_path: string;
    /**
     *
     * @type {string}
     * @memberof CollectionSourceSummary
     */
    support_type: string;
    /**
     *
     * @type {string}
     * @memberof CollectionSourceSummary
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface CollectionValidationRequest
 */
export interface CollectionValidationRequest {
    /**
     *
     * @type {string}
     * @memberof CollectionValidationRequest
     */
    workspace_id: string;
    /**
     *
     * @type {object}
     * @memberof CollectionValidationRequest
     */
    query: object;
    /**
     *
     * @type {CollectionValidationRequestViewTypeEnum}
     * @memberof CollectionValidationRequest
     */
    view_type: CollectionValidationRequestViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionValidationRequest
     */
    view_config: object;
}


/**
 * @export
 */
export const CollectionValidationRequestViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionValidationRequestViewTypeEnum = typeof CollectionValidationRequestViewTypeEnum[keyof typeof CollectionValidationRequestViewTypeEnum];

/**
 *
 * @export
 * @interface CollectionValidationResponse
 */
export interface CollectionValidationResponse {
    /**
     *
     * @type {string}
     * @memberof CollectionValidationResponse
     */
    workspace_id: string;
    /**
     *
     * @type {CollectionValidationResponseValidEnum}
     * @memberof CollectionValidationResponse
     */
    valid: CollectionValidationResponseValidEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionValidationResponse
     */
    query: object;
    /**
     *
     * @type {string}
     * @memberof CollectionValidationResponse
     */
    query_hash: string;
    /**
     *
     * @type {CollectionValidationResponseViewTypeEnum}
     * @memberof CollectionValidationResponse
     */
    view_type: CollectionValidationResponseViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionValidationResponse
     */
    view_config: object;
}


/**
 * @export
 */
export const CollectionValidationResponseValidEnum = {
    True: true
} as const;
export type CollectionValidationResponseValidEnum = typeof CollectionValidationResponseValidEnum[keyof typeof CollectionValidationResponseValidEnum];

/**
 * @export
 */
export const CollectionValidationResponseViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionValidationResponseViewTypeEnum = typeof CollectionValidationResponseViewTypeEnum[keyof typeof CollectionValidationResponseViewTypeEnum];

/**
 *
 * @export
 * @interface CollectionVersionedRequest
 */
export interface CollectionVersionedRequest {
    /**
     *
     * @type {string}
     * @memberof CollectionVersionedRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof CollectionVersionedRequest
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof CollectionVersionedRequest
     */
    description?: string;
    /**
     *
     * @type {object}
     * @memberof CollectionVersionedRequest
     */
    query: object;
    /**
     *
     * @type {CollectionVersionedRequestViewTypeEnum}
     * @memberof CollectionVersionedRequest
     */
    view_type: CollectionVersionedRequestViewTypeEnum;
    /**
     *
     * @type {object}
     * @memberof CollectionVersionedRequest
     */
    view_config: object;
    /**
     *
     * @type {number}
     * @memberof CollectionVersionedRequest
     */
    expected_version: number;
}


/**
 * @export
 */
export const CollectionVersionedRequestViewTypeEnum = {
    List: 'LIST',
    Table: 'TABLE',
    CompactCard: 'COMPACT_CARD'
} as const;
export type CollectionVersionedRequestViewTypeEnum = typeof CollectionVersionedRequestViewTypeEnum[keyof typeof CollectionVersionedRequestViewTypeEnum];

/**
 *
 * @export
 * @interface CompleteInterviewRequest
 */
export interface CompleteInterviewRequest {
    /**
     *
     * @type {string}
     * @memberof CompleteInterviewRequest
     */
    workspace_id: string;
    /**
     *
     * @type {boolean}
     * @memberof CompleteInterviewRequest
     */
    manual_end?: boolean;
}
/**
 *
 * @export
 * @interface Conversation
 */
export interface Conversation {
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    workspace_id: string;
    /**
     *
     * @type {ConversationStatusEnum}
     * @memberof Conversation
     */
    status: ConversationStatusEnum;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    title?: string | null;
    /**
     *
     * @type {number}
     * @memberof Conversation
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    last_activity_at: string;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof Conversation
     */
    archived_at: string | null;
}


/**
 * @export
 */
export const ConversationStatusEnum = {
    Open: 'open',
    Archived: 'archived'
} as const;
export type ConversationStatusEnum = typeof ConversationStatusEnum[keyof typeof ConversationStatusEnum];

/**
 *
 * @export
 * @interface ConversationPage
 */
export interface ConversationPage {
    /**
     *
     * @type {Array<Conversation>}
     * @memberof ConversationPage
     */
    items: Array<Conversation>;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof ConversationPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface CreateAPITokenRequest
 */
export interface CreateAPITokenRequest {
    /**
     *
     * @type {string}
     * @memberof CreateAPITokenRequest
     */
    name: string;
    /**
     *
     * @type {Array<AuthCapability>}
     * @memberof CreateAPITokenRequest
     */
    scopes: Array<AuthCapability>;
    /**
     * Zero uses the configured API Token TTL.
     * @type {number}
     * @memberof CreateAPITokenRequest
     */
    expires_in_seconds?: number;
}
/**
 *
 * @export
 * @interface CreateAttachmentExport200Response
 */
export interface CreateAttachmentExport200Response {
    /**
     *
     * @type {AttachmentExportJob}
     * @memberof CreateAttachmentExport200Response
     */
    job: AttachmentExportJob;
    /**
     *
     * @type {CreateAttachmentExport200ResponseReplayedEnum}
     * @memberof CreateAttachmentExport200Response
     */
    replayed: CreateAttachmentExport200ResponseReplayedEnum;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof CreateAttachmentExport200Response
     */
    dispatch_pending: boolean;
}


/**
 * @export
 */
export const CreateAttachmentExport200ResponseReplayedEnum = {
    True: true
} as const;
export type CreateAttachmentExport200ResponseReplayedEnum = typeof CreateAttachmentExport200ResponseReplayedEnum[keyof typeof CreateAttachmentExport200ResponseReplayedEnum];

/**
 *
 * @export
 * @interface CreateAttachmentExport202Response
 */
export interface CreateAttachmentExport202Response {
    /**
     *
     * @type {AttachmentExportJob}
     * @memberof CreateAttachmentExport202Response
     */
    job: AttachmentExportJob;
    /**
     *
     * @type {CreateAttachmentExport202ResponseReplayedEnum}
     * @memberof CreateAttachmentExport202Response
     */
    replayed: CreateAttachmentExport202ResponseReplayedEnum;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof CreateAttachmentExport202Response
     */
    dispatch_pending: boolean;
}


/**
 * @export
 */
export const CreateAttachmentExport202ResponseReplayedEnum = {
    False: false
} as const;
export type CreateAttachmentExport202ResponseReplayedEnum = typeof CreateAttachmentExport202ResponseReplayedEnum[keyof typeof CreateAttachmentExport202ResponseReplayedEnum];

/**
 * @type CreateCaptureRequest
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type CreateCaptureRequest = TextCapture | URLCapture;

/**
 *
 * @export
 * @interface CreateConversationRequest
 */
export interface CreateConversationRequest {
    /**
     *
     * @type {string}
     * @memberof CreateConversationRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof CreateConversationRequest
     */
    title?: string | null;
}
/**
 * @type CreateDownstreamUpdateProposalRequest
 * Selects exactly one owner-backed target from the current impact-report/v2. Unknown or duplicate JSON fields are rejected by the HTTP boundary.
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type CreateDownstreamUpdateProposalRequest = CreateDownstreamUpdateProposalRequestOneOf | CreateDownstreamUpdateProposalRequestOneOf1;

/**
 *
 * @export
 * @interface CreateDownstreamUpdateProposalRequestOneOf
 */
export interface CreateDownstreamUpdateProposalRequestOneOf {
    /**
     *
     * @type {CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum}
     * @memberof CreateDownstreamUpdateProposalRequestOneOf
     */
    target_type?: CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum;
    /**
     *
     * @type {CreateDownstreamUpdateProposalRequestOneOfActionEnum}
     * @memberof CreateDownstreamUpdateProposalRequestOneOf
     */
    action?: CreateDownstreamUpdateProposalRequestOneOfActionEnum;
}


/**
 * @export
 */
export const CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum = {
    Artifact: 'ARTIFACT'
} as const;
export type CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum = typeof CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum[keyof typeof CreateDownstreamUpdateProposalRequestOneOfTargetTypeEnum];

/**
 * @export
 */
export const CreateDownstreamUpdateProposalRequestOneOfActionEnum = {
    RegenerateArtifact: 'REGENERATE_ARTIFACT'
} as const;
export type CreateDownstreamUpdateProposalRequestOneOfActionEnum = typeof CreateDownstreamUpdateProposalRequestOneOfActionEnum[keyof typeof CreateDownstreamUpdateProposalRequestOneOfActionEnum];

/**
 *
 * @export
 * @interface CreateDownstreamUpdateProposalRequestOneOf1
 */
export interface CreateDownstreamUpdateProposalRequestOneOf1 {
    /**
     *
     * @type {CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum}
     * @memberof CreateDownstreamUpdateProposalRequestOneOf1
     */
    target_type?: CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum;
    /**
     *
     * @type {CreateDownstreamUpdateProposalRequestOneOf1ActionEnum}
     * @memberof CreateDownstreamUpdateProposalRequestOneOf1
     */
    action?: CreateDownstreamUpdateProposalRequestOneOf1ActionEnum;
}


/**
 * @export
 */
export const CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum = {
    ReviewCard: 'REVIEW_CARD'
} as const;
export type CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum = typeof CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum[keyof typeof CreateDownstreamUpdateProposalRequestOneOf1TargetTypeEnum];

/**
 * @export
 */
export const CreateDownstreamUpdateProposalRequestOneOf1ActionEnum = {
    RevalidateReviewCard: 'REVALIDATE_REVIEW_CARD'
} as const;
export type CreateDownstreamUpdateProposalRequestOneOf1ActionEnum = typeof CreateDownstreamUpdateProposalRequestOneOf1ActionEnum[keyof typeof CreateDownstreamUpdateProposalRequestOneOf1ActionEnum];

/**
 *
 * @export
 * @interface CreateExport200Response
 */
export interface CreateExport200Response {
    /**
     *
     * @type {ExportJob}
     * @memberof CreateExport200Response
     */
    job: ExportJob;
    /**
     *
     * @type {CreateExport200ResponseReplayedEnum}
     * @memberof CreateExport200Response
     */
    replayed: CreateExport200ResponseReplayedEnum;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof CreateExport200Response
     */
    dispatch_pending: boolean;
}


/**
 * @export
 */
export const CreateExport200ResponseReplayedEnum = {
    True: true
} as const;
export type CreateExport200ResponseReplayedEnum = typeof CreateExport200ResponseReplayedEnum[keyof typeof CreateExport200ResponseReplayedEnum];

/**
 *
 * @export
 * @interface CreateExport202Response
 */
export interface CreateExport202Response {
    /**
     *
     * @type {ExportJob}
     * @memberof CreateExport202Response
     */
    job: ExportJob;
    /**
     *
     * @type {CreateExport202ResponseReplayedEnum}
     * @memberof CreateExport202Response
     */
    replayed: CreateExport202ResponseReplayedEnum;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof CreateExport202Response
     */
    dispatch_pending: boolean;
}


/**
 * @export
 */
export const CreateExport202ResponseReplayedEnum = {
    False: false
} as const;
export type CreateExport202ResponseReplayedEnum = typeof CreateExport202ResponseReplayedEnum[keyof typeof CreateExport202ResponseReplayedEnum];

/**
 *
 * @export
 * @interface CreateImpactDownstreamUpdateProposal200Response
 */
export interface CreateImpactDownstreamUpdateProposal200Response {
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    proposal_type: CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    workspace_id: string;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal200ResponseStatusEnum}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    status: CreateImpactDownstreamUpdateProposal200ResponseStatusEnum;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    risk_level: CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {DownstreamUpdateRevision}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    revision: DownstreamUpdateRevision;
    /**
     *
     * @type {NonFileApproval}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    approval: NonFileApproval;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    replayed: CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal200Response
     */
    updated_at: string;
}


/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum = {
    DownstreamUpdate: 'downstream_update'
} as const;
export type CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum = typeof CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum[keyof typeof CreateImpactDownstreamUpdateProposal200ResponseProposalTypeEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal200ResponseStatusEnum = {
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type CreateImpactDownstreamUpdateProposal200ResponseStatusEnum = typeof CreateImpactDownstreamUpdateProposal200ResponseStatusEnum[keyof typeof CreateImpactDownstreamUpdateProposal200ResponseStatusEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum = typeof CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum[keyof typeof CreateImpactDownstreamUpdateProposal200ResponseRiskLevelEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum = {
    True: true
} as const;
export type CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum = typeof CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum[keyof typeof CreateImpactDownstreamUpdateProposal200ResponseReplayedEnum];

/**
 *
 * @export
 * @interface CreateImpactDownstreamUpdateProposal201Response
 */
export interface CreateImpactDownstreamUpdateProposal201Response {
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    proposal_type: CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    workspace_id: string;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal201ResponseStatusEnum}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    status: CreateImpactDownstreamUpdateProposal201ResponseStatusEnum;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    risk_level: CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {DownstreamUpdateRevision}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    revision: DownstreamUpdateRevision;
    /**
     *
     * @type {null}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    approval: null;
    /**
     *
     * @type {CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    replayed: CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof CreateImpactDownstreamUpdateProposal201Response
     */
    updated_at: string;
}


/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum = {
    DownstreamUpdate: 'downstream_update'
} as const;
export type CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum = typeof CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum[keyof typeof CreateImpactDownstreamUpdateProposal201ResponseProposalTypeEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal201ResponseStatusEnum = {
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type CreateImpactDownstreamUpdateProposal201ResponseStatusEnum = typeof CreateImpactDownstreamUpdateProposal201ResponseStatusEnum[keyof typeof CreateImpactDownstreamUpdateProposal201ResponseStatusEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum = typeof CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum[keyof typeof CreateImpactDownstreamUpdateProposal201ResponseRiskLevelEnum];

/**
 * @export
 */
export const CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum = {
    False: false
} as const;
export type CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum = typeof CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum[keyof typeof CreateImpactDownstreamUpdateProposal201ResponseReplayedEnum];

/**
 *
 * @export
 * @interface CreateMemoryCandidateRequest
 */
export interface CreateMemoryCandidateRequest {
    /**
     *
     * @type {string}
     * @memberof CreateMemoryCandidateRequest
     */
    workspace_id: string;
    /**
     *
     * @type {CreateMemoryCandidateRequestTypeEnum}
     * @memberof CreateMemoryCandidateRequest
     */
    type: CreateMemoryCandidateRequestTypeEnum;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof CreateMemoryCandidateRequest
     */
    content: { [key: string]: JSONValue; };
    /**
     *
     * @type {string}
     * @memberof CreateMemoryCandidateRequest
     */
    task_scope_id?: string;
    /**
     *
     * @type {string}
     * @memberof CreateMemoryCandidateRequest
     */
    expires_at?: string;
}


/**
 * @export
 */
export const CreateMemoryCandidateRequestTypeEnum = {
    Preference: 'PREFERENCE',
    Episodic: 'EPISODIC',
    Goal: 'GOAL',
    Feedback: 'FEEDBACK'
} as const;
export type CreateMemoryCandidateRequestTypeEnum = typeof CreateMemoryCandidateRequestTypeEnum[keyof typeof CreateMemoryCandidateRequestTypeEnum];

/**
 *
 * @export
 * @interface CreateProposalRequest
 */
export interface CreateProposalRequest {
    /**
     * Workspace-relative POSIX path; absolute paths, backslashes and traversal are rejected.
     * @type {string}
     * @memberof CreateProposalRequest
     */
    target_path: string;
    /**
     *
     * @type {string}
     * @memberof CreateProposalRequest
     */
    base_hash: string;
    /**
     *
     * @type {string}
     * @memberof CreateProposalRequest
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof CreateProposalRequest
     */
    evidence_summary: string;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof CreateProposalRequest
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {string}
     * @memberof CreateProposalRequest
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof CreateProposalRequest
     */
    rollback_plan: string;
}


/**
 *
 * @export
 * @interface CreateReviewLearningPathRequest
 */
export interface CreateReviewLearningPathRequest {
    /**
     *
     * @type {string}
     * @memberof CreateReviewLearningPathRequest
     */
    workspace_id: string;
}
/**
 *
 * @export
 * @interface CreateWorkspaceRequest
 */
export interface CreateWorkspaceRequest {
    /**
     *
     * @type {string}
     * @memberof CreateWorkspaceRequest
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof CreateWorkspaceRequest
     */
    root_path: string;
    /**
     *
     * @type {boolean}
     * @memberof CreateWorkspaceRequest
     */
    initialize_git: boolean;
}
/**
 *
 * @export
 * @interface DatabaseStatus
 */
export interface DatabaseStatus {
    /**
     *
     * @type {DatabaseStatusStatusEnum}
     * @memberof DatabaseStatus
     */
    status: DatabaseStatusStatusEnum;
    /**
     *
     * @type {string}
     * @memberof DatabaseStatus
     */
    message?: string;
}


/**
 * @export
 */
export const DatabaseStatusStatusEnum = {
    Ready: 'ready',
    Unavailable: 'unavailable'
} as const;
export type DatabaseStatusStatusEnum = typeof DatabaseStatusStatusEnum[keyof typeof DatabaseStatusStatusEnum];

/**
 *
 * @export
 * @interface DisabledChatDraft
 */
export interface DisabledChatDraft {
    /**
     *
     * @type {DisabledChatDraftProviderEnum}
     * @memberof DisabledChatDraft
     */
    provider?: DisabledChatDraftProviderEnum;
    /**
     *
     * @type {DisabledChatDraftBaseUrlEnum}
     * @memberof DisabledChatDraft
     */
    base_url?: DisabledChatDraftBaseUrlEnum;
    /**
     *
     * @type {DisabledChatDraftModelEnum}
     * @memberof DisabledChatDraft
     */
    model?: DisabledChatDraftModelEnum;
    /**
     *
     * @type {DisabledChatDraftModelVersionEnum}
     * @memberof DisabledChatDraft
     */
    model_version?: DisabledChatDraftModelVersionEnum;
}


/**
 * @export
 */
export const DisabledChatDraftProviderEnum = {
    Disabled: 'disabled'
} as const;
export type DisabledChatDraftProviderEnum = typeof DisabledChatDraftProviderEnum[keyof typeof DisabledChatDraftProviderEnum];

/**
 * @export
 */
export const DisabledChatDraftBaseUrlEnum = {
    Empty: ''
} as const;
export type DisabledChatDraftBaseUrlEnum = typeof DisabledChatDraftBaseUrlEnum[keyof typeof DisabledChatDraftBaseUrlEnum];

/**
 * @export
 */
export const DisabledChatDraftModelEnum = {
    Empty: ''
} as const;
export type DisabledChatDraftModelEnum = typeof DisabledChatDraftModelEnum[keyof typeof DisabledChatDraftModelEnum];

/**
 * @export
 */
export const DisabledChatDraftModelVersionEnum = {
    Empty: ''
} as const;
export type DisabledChatDraftModelVersionEnum = typeof DisabledChatDraftModelVersionEnum[keyof typeof DisabledChatDraftModelVersionEnum];

/**
 *
 * @export
 * @interface DisabledChatSettings
 */
export interface DisabledChatSettings {
    /**
     *
     * @type {DisabledChatSettingsProviderEnum}
     * @memberof DisabledChatSettings
     */
    provider?: DisabledChatSettingsProviderEnum;
    /**
     *
     * @type {DisabledChatSettingsBaseUrlEnum}
     * @memberof DisabledChatSettings
     */
    base_url?: DisabledChatSettingsBaseUrlEnum;
    /**
     *
     * @type {DisabledChatSettingsModelEnum}
     * @memberof DisabledChatSettings
     */
    model?: DisabledChatSettingsModelEnum;
    /**
     *
     * @type {DisabledChatSettingsModelVersionEnum}
     * @memberof DisabledChatSettings
     */
    model_version?: DisabledChatSettingsModelVersionEnum;
    /**
     *
     * @type {DisabledChatSettingsApiKeyConfiguredEnum}
     * @memberof DisabledChatSettings
     */
    api_key_configured?: DisabledChatSettingsApiKeyConfiguredEnum;
}


/**
 * @export
 */
export const DisabledChatSettingsProviderEnum = {
    Disabled: 'disabled'
} as const;
export type DisabledChatSettingsProviderEnum = typeof DisabledChatSettingsProviderEnum[keyof typeof DisabledChatSettingsProviderEnum];

/**
 * @export
 */
export const DisabledChatSettingsBaseUrlEnum = {
    Empty: ''
} as const;
export type DisabledChatSettingsBaseUrlEnum = typeof DisabledChatSettingsBaseUrlEnum[keyof typeof DisabledChatSettingsBaseUrlEnum];

/**
 * @export
 */
export const DisabledChatSettingsModelEnum = {
    Empty: ''
} as const;
export type DisabledChatSettingsModelEnum = typeof DisabledChatSettingsModelEnum[keyof typeof DisabledChatSettingsModelEnum];

/**
 * @export
 */
export const DisabledChatSettingsModelVersionEnum = {
    Empty: ''
} as const;
export type DisabledChatSettingsModelVersionEnum = typeof DisabledChatSettingsModelVersionEnum[keyof typeof DisabledChatSettingsModelVersionEnum];

/**
 * @export
 */
export const DisabledChatSettingsApiKeyConfiguredEnum = {
    False: false
} as const;
export type DisabledChatSettingsApiKeyConfiguredEnum = typeof DisabledChatSettingsApiKeyConfiguredEnum[keyof typeof DisabledChatSettingsApiKeyConfiguredEnum];

/**
 *
 * @export
 * @interface DisabledEmbeddingDraft
 */
export interface DisabledEmbeddingDraft {
    /**
     *
     * @type {DisabledEmbeddingDraftProviderEnum}
     * @memberof DisabledEmbeddingDraft
     */
    provider?: DisabledEmbeddingDraftProviderEnum;
    /**
     *
     * @type {DisabledEmbeddingDraftBaseUrlEnum}
     * @memberof DisabledEmbeddingDraft
     */
    base_url?: DisabledEmbeddingDraftBaseUrlEnum;
    /**
     *
     * @type {DisabledEmbeddingDraftModelEnum}
     * @memberof DisabledEmbeddingDraft
     */
    model?: DisabledEmbeddingDraftModelEnum;
    /**
     *
     * @type {DisabledEmbeddingDraftDimensionsEnum}
     * @memberof DisabledEmbeddingDraft
     */
    dimensions?: DisabledEmbeddingDraftDimensionsEnum;
}


/**
 * @export
 */
export const DisabledEmbeddingDraftProviderEnum = {
    Disabled: 'disabled'
} as const;
export type DisabledEmbeddingDraftProviderEnum = typeof DisabledEmbeddingDraftProviderEnum[keyof typeof DisabledEmbeddingDraftProviderEnum];

/**
 * @export
 */
export const DisabledEmbeddingDraftBaseUrlEnum = {
    Empty: ''
} as const;
export type DisabledEmbeddingDraftBaseUrlEnum = typeof DisabledEmbeddingDraftBaseUrlEnum[keyof typeof DisabledEmbeddingDraftBaseUrlEnum];

/**
 * @export
 */
export const DisabledEmbeddingDraftModelEnum = {
    Empty: ''
} as const;
export type DisabledEmbeddingDraftModelEnum = typeof DisabledEmbeddingDraftModelEnum[keyof typeof DisabledEmbeddingDraftModelEnum];

/**
 * @export
 */
export const DisabledEmbeddingDraftDimensionsEnum = {
    NUMBER_0: 0
} as const;
export type DisabledEmbeddingDraftDimensionsEnum = typeof DisabledEmbeddingDraftDimensionsEnum[keyof typeof DisabledEmbeddingDraftDimensionsEnum];

/**
 *
 * @export
 * @interface DisabledEmbeddingSettings
 */
export interface DisabledEmbeddingSettings {
    /**
     *
     * @type {DisabledEmbeddingSettingsProviderEnum}
     * @memberof DisabledEmbeddingSettings
     */
    provider?: DisabledEmbeddingSettingsProviderEnum;
    /**
     *
     * @type {DisabledEmbeddingSettingsBaseUrlEnum}
     * @memberof DisabledEmbeddingSettings
     */
    base_url?: DisabledEmbeddingSettingsBaseUrlEnum;
    /**
     *
     * @type {DisabledEmbeddingSettingsModelEnum}
     * @memberof DisabledEmbeddingSettings
     */
    model?: DisabledEmbeddingSettingsModelEnum;
    /**
     *
     * @type {DisabledEmbeddingSettingsDimensionsEnum}
     * @memberof DisabledEmbeddingSettings
     */
    dimensions?: DisabledEmbeddingSettingsDimensionsEnum;
    /**
     *
     * @type {DisabledEmbeddingSettingsApiKeyConfiguredEnum}
     * @memberof DisabledEmbeddingSettings
     */
    api_key_configured?: DisabledEmbeddingSettingsApiKeyConfiguredEnum;
}


/**
 * @export
 */
export const DisabledEmbeddingSettingsProviderEnum = {
    Disabled: 'disabled'
} as const;
export type DisabledEmbeddingSettingsProviderEnum = typeof DisabledEmbeddingSettingsProviderEnum[keyof typeof DisabledEmbeddingSettingsProviderEnum];

/**
 * @export
 */
export const DisabledEmbeddingSettingsBaseUrlEnum = {
    Empty: ''
} as const;
export type DisabledEmbeddingSettingsBaseUrlEnum = typeof DisabledEmbeddingSettingsBaseUrlEnum[keyof typeof DisabledEmbeddingSettingsBaseUrlEnum];

/**
 * @export
 */
export const DisabledEmbeddingSettingsModelEnum = {
    Empty: ''
} as const;
export type DisabledEmbeddingSettingsModelEnum = typeof DisabledEmbeddingSettingsModelEnum[keyof typeof DisabledEmbeddingSettingsModelEnum];

/**
 * @export
 */
export const DisabledEmbeddingSettingsDimensionsEnum = {
    NUMBER_0: 0
} as const;
export type DisabledEmbeddingSettingsDimensionsEnum = typeof DisabledEmbeddingSettingsDimensionsEnum[keyof typeof DisabledEmbeddingSettingsDimensionsEnum];

/**
 * @export
 */
export const DisabledEmbeddingSettingsApiKeyConfiguredEnum = {
    False: false
} as const;
export type DisabledEmbeddingSettingsApiKeyConfiguredEnum = typeof DisabledEmbeddingSettingsApiKeyConfiguredEnum[keyof typeof DisabledEmbeddingSettingsApiKeyConfiguredEnum];

/**
 *
 * @export
 * @interface DocumentDraft
 */
export interface DocumentDraft {
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    canonical_path: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    title: string;
    /**
     *
     * @type {DocumentDraftLifecycleStatusEnum}
     * @memberof DocumentDraft
     */
    lifecycle_status: DocumentDraftLifecycleStatusEnum;
    /**
     *
     * @type {DocumentDraftCurrentPublishedRevisionId}
     * @memberof DocumentDraft
     */
    current_published_revision_id: DocumentDraftCurrentPublishedRevisionId;
    /**
     *
     * @type {number}
     * @memberof DocumentDraft
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraft
     */
    updated_at: string;
}


/**
 * @export
 */
export const DocumentDraftLifecycleStatusEnum = {
    Draft: 'DRAFT',
    Published: 'PUBLISHED',
    Archived: 'ARCHIVED',
    Deleted: 'DELETED'
} as const;
export type DocumentDraftLifecycleStatusEnum = typeof DocumentDraftLifecycleStatusEnum[keyof typeof DocumentDraftLifecycleStatusEnum];

/**
 * @type DocumentDraftCurrentPublishedRevisionId
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type DocumentDraftCurrentPublishedRevisionId = null | string;

/**
 *
 * @export
 * @interface DocumentDraftDetail
 */
export interface DocumentDraftDetail {
    /**
     *
     * @type {DocumentDraft}
     * @memberof DocumentDraftDetail
     */
    document: DocumentDraft;
    /**
     *
     * @type {ArticleRevision}
     * @memberof DocumentDraftDetail
     */
    current_revision: ArticleRevision | null;
    /**
     *
     * @type {PublicationBinding}
     * @memberof DocumentDraftDetail
     */
    publication: PublicationBinding | null;
}
/**
 *
 * @export
 * @interface DocumentDraftPage
 */
export interface DocumentDraftPage {
    /**
     *
     * @type {string}
     * @memberof DocumentDraftPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<DocumentDraftSummary>}
     * @memberof DocumentDraftPage
     */
    items: Array<DocumentDraftSummary>;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface DocumentDraftSummary
 */
export interface DocumentDraftSummary {
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    canonical_path: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    title: string;
    /**
     *
     * @type {DocumentDraftSummaryLifecycleStatusEnum}
     * @memberof DocumentDraftSummary
     */
    lifecycle_status: DocumentDraftSummaryLifecycleStatusEnum;
    /**
     *
     * @type {null}
     * @memberof DocumentDraftSummary
     */
    current_published_revision_id: null;
    /**
     *
     * @type {number}
     * @memberof DocumentDraftSummary
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof DocumentDraftSummary
     */
    updated_at: string;
}


/**
 * @export
 */
export const DocumentDraftSummaryLifecycleStatusEnum = {
    Draft: 'DRAFT'
} as const;
export type DocumentDraftSummaryLifecycleStatusEnum = typeof DocumentDraftSummaryLifecycleStatusEnum[keyof typeof DocumentDraftSummaryLifecycleStatusEnum];

/**
 *
 * @export
 * @interface DocumentHistoryCompare
 */
export interface DocumentHistoryCompare {
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    path: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    head: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    left: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    right: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    left_content: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    right_content: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    patch: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCompare
     */
    diff_hash: string;
}
/**
 *
 * @export
 * @interface DocumentHistoryCurrentEntry
 */
export interface DocumentHistoryCurrentEntry {
    /**
     *
     * @type {DocumentHistoryCurrentEntryKindEnum}
     * @memberof DocumentHistoryCurrentEntry
     */
    kind: DocumentHistoryCurrentEntryKindEnum;
    /**
     *
     * @type {null}
     * @memberof DocumentHistoryCurrentEntry
     */
    commit: null;
    /**
     *
     * @type {Array<string>}
     * @memberof DocumentHistoryCurrentEntry
     */
    parent_commits: Array<string>;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCurrentEntry
     */
    author_name: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCurrentEntry
     */
    author_email: string;
    /**
     *
     * @type {null}
     * @memberof DocumentHistoryCurrentEntry
     */
    committed_at: null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryCurrentEntry
     */
    summary: string;
}


/**
 * @export
 */
export const DocumentHistoryCurrentEntryKindEnum = {
    CurrentChange: 'CURRENT_CHANGE'
} as const;
export type DocumentHistoryCurrentEntryKindEnum = typeof DocumentHistoryCurrentEntryKindEnum[keyof typeof DocumentHistoryCurrentEntryKindEnum];

/**
 * @type DocumentHistoryEntry
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type DocumentHistoryEntry = { kind: 'CURRENT_CHANGE' } & DocumentHistoryCurrentEntry | { kind: 'EXTERNAL' } & DocumentHistoryExternalEntry | { kind: 'MANAGED' } & DocumentHistoryManagedEntry;

/**
 *
 * @export
 * @interface DocumentHistoryExternalEntry
 */
export interface DocumentHistoryExternalEntry {
    /**
     *
     * @type {DocumentHistoryExternalEntryKindEnum}
     * @memberof DocumentHistoryExternalEntry
     */
    kind: DocumentHistoryExternalEntryKindEnum;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryExternalEntry
     */
    commit: string;
    /**
     *
     * @type {Array<string>}
     * @memberof DocumentHistoryExternalEntry
     */
    parent_commits: Array<string>;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryExternalEntry
     */
    author_name: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryExternalEntry
     */
    author_email: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryExternalEntry
     */
    committed_at: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryExternalEntry
     */
    summary: string;
}


/**
 * @export
 */
export const DocumentHistoryExternalEntryKindEnum = {
    External: 'EXTERNAL'
} as const;
export type DocumentHistoryExternalEntryKindEnum = typeof DocumentHistoryExternalEntryKindEnum[keyof typeof DocumentHistoryExternalEntryKindEnum];

/**
 * At least one Article Revision or complete Proposal writeback binding is present; missing historical relations remain explicit nulls.
 * @export
 * @interface DocumentHistoryManagedEntry
 */
export interface DocumentHistoryManagedEntry {
    /**
     *
     * @type {DocumentHistoryManagedEntryKindEnum}
     * @memberof DocumentHistoryManagedEntry
     */
    kind: DocumentHistoryManagedEntryKindEnum;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    commit: string;
    /**
     *
     * @type {Array<string>}
     * @memberof DocumentHistoryManagedEntry
     */
    parent_commits: Array<string>;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    author_name: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    author_email: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    committed_at: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    summary: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    article_revision_id: string | null;
    /**
     *
     * @type {number}
     * @memberof DocumentHistoryManagedEntry
     */
    article_revision_no: number | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    proposal_id: string | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    proposal_revision_id: string | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    approval_id: string | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    workflow_run_id: string | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    writeback_id: string | null;
    /**
     *
     * @type {DocumentHistoryManagedEntryProposalTypeEnum}
     * @memberof DocumentHistoryManagedEntry
     */
    proposal_type: DocumentHistoryManagedEntryProposalTypeEnum | null;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryManagedEntry
     */
    approval_decided_at: string | null;
}


/**
 * @export
 */
export const DocumentHistoryManagedEntryKindEnum = {
    Managed: 'MANAGED'
} as const;
export type DocumentHistoryManagedEntryKindEnum = typeof DocumentHistoryManagedEntryKindEnum[keyof typeof DocumentHistoryManagedEntryKindEnum];

/**
 * @export
 */
export const DocumentHistoryManagedEntryProposalTypeEnum = {
    FilePatch: 'file_patch',
    RestoreDocument: 'restore_document'
} as const;
export type DocumentHistoryManagedEntryProposalTypeEnum = typeof DocumentHistoryManagedEntryProposalTypeEnum[keyof typeof DocumentHistoryManagedEntryProposalTypeEnum];

/**
 *
 * @export
 * @interface DocumentHistoryPage
 */
export interface DocumentHistoryPage {
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    document_id: string;
    /**
     *
     * @type {number}
     * @memberof DocumentHistoryPage
     */
    document_version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    path: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    branch: string;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    head: string;
    /**
     *
     * @type {boolean}
     * @memberof DocumentHistoryPage
     */
    dirty: boolean;
    /**
     *
     * @type {Array<DocumentHistoryEntry>}
     * @memberof DocumentHistoryPage
     */
    items: Array<DocumentHistoryEntry>;
    /**
     *
     * @type {string}
     * @memberof DocumentHistoryPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface DocumentKnowledgeProfile
 */
export interface DocumentKnowledgeProfile {
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    capture_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    current_revision_id?: string;
    /**
     *
     * @type {KnowledgeProfileStatus}
     * @memberof DocumentKnowledgeProfile
     */
    status: KnowledgeProfileStatus;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    error_code?: string;
    /**
     *
     * @type {boolean}
     * @memberof DocumentKnowledgeProfile
     */
    retryable: boolean;
    /**
     *
     * @type {number}
     * @memberof DocumentKnowledgeProfile
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfile
     */
    updated_at: string;
}


/**
 *
 * @export
 * @interface DocumentKnowledgeProfileContent
 */
export interface DocumentKnowledgeProfileContent {
    /**
     *
     * @type {string}
     * @memberof DocumentKnowledgeProfileContent
     */
    summary: string;
    /**
     *
     * @type {Array<KnowledgeProfileCandidate>}
     * @memberof DocumentKnowledgeProfileContent
     */
    topics: Array<KnowledgeProfileCandidate>;
    /**
     *
     * @type {Array<KnowledgeProfileCandidate>}
     * @memberof DocumentKnowledgeProfileContent
     */
    terms: Array<KnowledgeProfileCandidate>;
    /**
     *
     * @type {Array<KnowledgeProfilePoint>}
     * @memberof DocumentKnowledgeProfileContent
     */
    knowledge_points: Array<KnowledgeProfilePoint>;
    /**
     *
     * @type {Array<KnowledgeProfilePoint>}
     * @memberof DocumentKnowledgeProfileContent
     */
    examples: Array<KnowledgeProfilePoint>;
}
/**
 *
 * @export
 * @interface DocumentKnowledgeProfileResponse
 */
export interface DocumentKnowledgeProfileResponse {
    /**
     *
     * @type {DocumentKnowledgeProfile}
     * @memberof DocumentKnowledgeProfileResponse
     */
    profile: DocumentKnowledgeProfile;
    /**
     *
     * @type {KnowledgeProfileRevision}
     * @memberof DocumentKnowledgeProfileResponse
     */
    revision: KnowledgeProfileRevision | null;
}
/**
 *
 * @export
 * @interface DocumentRestorePreview
 */
export interface DocumentRestorePreview {
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    path: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    target_commit: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    expected_head: string;
    /**
     *
     * @type {number}
     * @memberof DocumentRestorePreview
     */
    expected_document_version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    current_content_hash: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    target_content_hash: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    current_content: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    target_content: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    patch: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    diff_hash: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreview
     */
    preview_hash: string;
    /**
     *
     * @type {boolean}
     * @memberof DocumentRestorePreview
     */
    blocked_by_dirty_worktree: boolean;
}
/**
 *
 * @export
 * @interface DocumentRestorePreviewRequest
 */
export interface DocumentRestorePreviewRequest {
    /**
     *
     * @type {string}
     * @memberof DocumentRestorePreviewRequest
     */
    target_commit: string;
    /**
     *
     * @type {number}
     * @memberof DocumentRestorePreviewRequest
     */
    expected_document_version: number;
}
/**
 *
 * @export
 * @interface DocumentRestoreProposalRequest
 */
export interface DocumentRestoreProposalRequest {
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalRequest
     */
    target_commit: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalRequest
     */
    expected_head: string;
    /**
     *
     * @type {number}
     * @memberof DocumentRestoreProposalRequest
     */
    expected_document_version: number;
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalRequest
     */
    preview_hash: string;
}
/**
 *
 * @export
 * @interface DocumentRestoreProposalResult
 */
export interface DocumentRestoreProposalResult {
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalResult
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalResult
     */
    proposal_revision_id: string;
    /**
     *
     * @type {DocumentRestoreProposalResultProposalTypeEnum}
     * @memberof DocumentRestoreProposalResult
     */
    proposal_type: DocumentRestoreProposalResultProposalTypeEnum;
    /**
     *
     * @type {DocumentRestoreProposalResultStatusEnum}
     * @memberof DocumentRestoreProposalResult
     */
    status: DocumentRestoreProposalResultStatusEnum;
    /**
     *
     * @type {string}
     * @memberof DocumentRestoreProposalResult
     */
    change_hash: string;
    /**
     *
     * @type {boolean}
     * @memberof DocumentRestoreProposalResult
     */
    replayed: boolean;
}


/**
 * @export
 */
export const DocumentRestoreProposalResultProposalTypeEnum = {
    RestoreDocument: 'restore_document'
} as const;
export type DocumentRestoreProposalResultProposalTypeEnum = typeof DocumentRestoreProposalResultProposalTypeEnum[keyof typeof DocumentRestoreProposalResultProposalTypeEnum];

/**
 * @export
 */
export const DocumentRestoreProposalResultStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type DocumentRestoreProposalResultStatusEnum = typeof DocumentRestoreProposalResultStatusEnum[keyof typeof DocumentRestoreProposalResultStatusEnum];

/**
 * @type DownstreamUpdate
 * Frozen approval intent. The payload is not an apply command and never carries a write authorization or Workflow binding.
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type DownstreamUpdate = DownstreamUpdateOneOf | DownstreamUpdateOneOf1;

/**
 *
 * @export
 * @interface DownstreamUpdateOneOf
 */
export interface DownstreamUpdateOneOf {
    /**
     *
     * @type {DownstreamUpdateOneOfTargetTypeEnum}
     * @memberof DownstreamUpdateOneOf
     */
    target_type?: DownstreamUpdateOneOfTargetTypeEnum;
    /**
     *
     * @type {DownstreamUpdateOneOfActionEnum}
     * @memberof DownstreamUpdateOneOf
     */
    action?: DownstreamUpdateOneOfActionEnum;
}


/**
 * @export
 */
export const DownstreamUpdateOneOfTargetTypeEnum = {
    Artifact: 'ARTIFACT'
} as const;
export type DownstreamUpdateOneOfTargetTypeEnum = typeof DownstreamUpdateOneOfTargetTypeEnum[keyof typeof DownstreamUpdateOneOfTargetTypeEnum];

/**
 * @export
 */
export const DownstreamUpdateOneOfActionEnum = {
    RegenerateArtifact: 'REGENERATE_ARTIFACT'
} as const;
export type DownstreamUpdateOneOfActionEnum = typeof DownstreamUpdateOneOfActionEnum[keyof typeof DownstreamUpdateOneOfActionEnum];

/**
 *
 * @export
 * @interface DownstreamUpdateOneOf1
 */
export interface DownstreamUpdateOneOf1 {
    /**
     *
     * @type {DownstreamUpdateOneOf1TargetTypeEnum}
     * @memberof DownstreamUpdateOneOf1
     */
    target_type?: DownstreamUpdateOneOf1TargetTypeEnum;
    /**
     *
     * @type {DownstreamUpdateOneOf1ActionEnum}
     * @memberof DownstreamUpdateOneOf1
     */
    action?: DownstreamUpdateOneOf1ActionEnum;
}


/**
 * @export
 */
export const DownstreamUpdateOneOf1TargetTypeEnum = {
    ReviewCard: 'REVIEW_CARD'
} as const;
export type DownstreamUpdateOneOf1TargetTypeEnum = typeof DownstreamUpdateOneOf1TargetTypeEnum[keyof typeof DownstreamUpdateOneOf1TargetTypeEnum];

/**
 * @export
 */
export const DownstreamUpdateOneOf1ActionEnum = {
    RevalidateReviewCard: 'REVALIDATE_REVIEW_CARD'
} as const;
export type DownstreamUpdateOneOf1ActionEnum = typeof DownstreamUpdateOneOf1ActionEnum[keyof typeof DownstreamUpdateOneOf1ActionEnum];

/**
 * An approval-only downstream owner update intent. Approved responses remain non-executable and omit every Git, Workflow, write-authorization and writeback field.
 * @export
 * @interface DownstreamUpdateProposal
 */
export interface DownstreamUpdateProposal {
    /**
     *
     * @type {DownstreamUpdateProposalProposalTypeEnum}
     * @memberof DownstreamUpdateProposal
     */
    proposal_type: DownstreamUpdateProposalProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposal
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposal
     */
    workspace_id: string;
    /**
     *
     * @type {DownstreamUpdateProposalStatusEnum}
     * @memberof DownstreamUpdateProposal
     */
    status: DownstreamUpdateProposalStatusEnum;
    /**
     *
     * @type {DownstreamUpdateProposalRiskLevelEnum}
     * @memberof DownstreamUpdateProposal
     */
    risk_level: DownstreamUpdateProposalRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof DownstreamUpdateProposal
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof DownstreamUpdateProposal
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {DownstreamUpdateRevision}
     * @memberof DownstreamUpdateProposal
     */
    revision: DownstreamUpdateRevision;
    /**
     *
     * @type {NonFileApproval}
     * @memberof DownstreamUpdateProposal
     */
    approval: NonFileApproval | null;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposal
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposal
     */
    updated_at: string;
}


/**
 * @export
 */
export const DownstreamUpdateProposalProposalTypeEnum = {
    DownstreamUpdate: 'downstream_update'
} as const;
export type DownstreamUpdateProposalProposalTypeEnum = typeof DownstreamUpdateProposalProposalTypeEnum[keyof typeof DownstreamUpdateProposalProposalTypeEnum];

/**
 * @export
 */
export const DownstreamUpdateProposalStatusEnum = {
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type DownstreamUpdateProposalStatusEnum = typeof DownstreamUpdateProposalStatusEnum[keyof typeof DownstreamUpdateProposalStatusEnum];

/**
 * @export
 */
export const DownstreamUpdateProposalRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type DownstreamUpdateProposalRiskLevelEnum = typeof DownstreamUpdateProposalRiskLevelEnum[keyof typeof DownstreamUpdateProposalRiskLevelEnum];

/**
 * Endpoint-only downstream Proposal response. replayed is intentionally absent from Proposal detail and list resources.
 * @export
 * @interface DownstreamUpdateProposalCreateResponse
 */
export interface DownstreamUpdateProposalCreateResponse {
    /**
     *
     * @type {DownstreamUpdateProposalCreateResponseProposalTypeEnum}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    proposal_type: DownstreamUpdateProposalCreateResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    workspace_id: string;
    /**
     *
     * @type {DownstreamUpdateProposalCreateResponseStatusEnum}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    status: DownstreamUpdateProposalCreateResponseStatusEnum;
    /**
     *
     * @type {DownstreamUpdateProposalCreateResponseRiskLevelEnum}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    risk_level: DownstreamUpdateProposalCreateResponseRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {DownstreamUpdateRevision}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    revision: DownstreamUpdateRevision;
    /**
     *
     * @type {NonFileApproval}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    approval: NonFileApproval | null;
    /**
     *
     * @type {boolean}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    replayed: boolean;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateProposalCreateResponse
     */
    updated_at: string;
}


/**
 * @export
 */
export const DownstreamUpdateProposalCreateResponseProposalTypeEnum = {
    DownstreamUpdate: 'downstream_update'
} as const;
export type DownstreamUpdateProposalCreateResponseProposalTypeEnum = typeof DownstreamUpdateProposalCreateResponseProposalTypeEnum[keyof typeof DownstreamUpdateProposalCreateResponseProposalTypeEnum];

/**
 * @export
 */
export const DownstreamUpdateProposalCreateResponseStatusEnum = {
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type DownstreamUpdateProposalCreateResponseStatusEnum = typeof DownstreamUpdateProposalCreateResponseStatusEnum[keyof typeof DownstreamUpdateProposalCreateResponseStatusEnum];

/**
 * @export
 */
export const DownstreamUpdateProposalCreateResponseRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type DownstreamUpdateProposalCreateResponseRiskLevelEnum = typeof DownstreamUpdateProposalCreateResponseRiskLevelEnum[keyof typeof DownstreamUpdateProposalCreateResponseRiskLevelEnum];

/**
 *
 * @export
 * @interface DownstreamUpdateRevision
 */
export interface DownstreamUpdateRevision {
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof DownstreamUpdateRevision
     */
    revision_no: number;
    /**
     *
     * @type {DownstreamUpdate}
     * @memberof DownstreamUpdateRevision
     */
    update: DownstreamUpdate;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateRevision
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface DownstreamUpdateSourceEvent
 */
export interface DownstreamUpdateSourceEvent {
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateSourceEvent
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof DownstreamUpdateSourceEvent
     */
    event_version: number;
}
/**
 *
 * @export
 * @interface DownstreamUpdateSourceReport
 */
export interface DownstreamUpdateSourceReport {
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateSourceReport
     */
    id: string;
    /**
     *
     * @type {DownstreamUpdateSourceReportAnalysisVersionEnum}
     * @memberof DownstreamUpdateSourceReport
     */
    analysis_version: DownstreamUpdateSourceReportAnalysisVersionEnum;
    /**
     *
     * @type {string}
     * @memberof DownstreamUpdateSourceReport
     */
    fingerprint: string;
}


/**
 * @export
 */
export const DownstreamUpdateSourceReportAnalysisVersionEnum = {
    ImpactAnalysisV2: 'impact-analysis/v2'
} as const;
export type DownstreamUpdateSourceReportAnalysisVersionEnum = typeof DownstreamUpdateSourceReportAnalysisVersionEnum[keyof typeof DownstreamUpdateSourceReportAnalysisVersionEnum];

/**
 *
 * @export
 * @interface EditMemoryRequest
 */
export interface EditMemoryRequest {
    /**
     *
     * @type {string}
     * @memberof EditMemoryRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof EditMemoryRequest
     */
    expected_version: number;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof EditMemoryRequest
     */
    content: { [key: string]: JSONValue; };
    /**
     *
     * @type {string}
     * @memberof EditMemoryRequest
     */
    task_scope_id?: string;
    /**
     *
     * @type {string}
     * @memberof EditMemoryRequest
     */
    expires_at?: string;
}
/**
 *
 * @export
 * @interface EmbeddingConnectionTest
 */
export interface EmbeddingConnectionTest {
    /**
     *
     * @type {EmbeddingConnectionTestTargetEnum}
     * @memberof EmbeddingConnectionTest
     */
    target?: EmbeddingConnectionTestTargetEnum;
    /**
     *
     * @type {ChatConnectionTestChat}
     * @memberof EmbeddingConnectionTest
     */
    embedding: ChatConnectionTestChat;
}


/**
 * @export
 */
export const EmbeddingConnectionTestTargetEnum = {
    Embedding: 'embedding'
} as const;
export type EmbeddingConnectionTestTargetEnum = typeof EmbeddingConnectionTestTargetEnum[keyof typeof EmbeddingConnectionTestTargetEnum];

/**
 *
 * @export
 * @interface EvidenceProvenance
 */
export interface EvidenceProvenance {
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    source_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    relative_path: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    captured_at: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    source_version_href: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceProvenance
     */
    source_span_href: string;
}
/**
 *
 * @export
 * @interface EvidenceScores
 */
export interface EvidenceScores {
    /**
     *
     * @type {LexicalScore}
     * @memberof EvidenceScores
     */
    lexical: LexicalScore | null;
    /**
     *
     * @type {VectorDistance}
     * @memberof EvidenceScores
     */
    vector: VectorDistance | null;
    /**
     *
     * @type {StageScore}
     * @memberof EvidenceScores
     */
    fusion: StageScore;
    /**
     *
     * @type {RerankScore}
     * @memberof EvidenceScores
     */
    rerank: RerankScore | null;
}
/**
 *
 * @export
 * @interface EvidenceSourceSpan
 */
export interface EvidenceSourceSpan {
    /**
     *
     * @type {EvidenceSourceVersion}
     * @memberof EvidenceSourceSpan
     */
    source_version: EvidenceSourceVersion;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    parse_projection_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    span_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    span_type: string;
    /**
     *
     * @type {number}
     * @memberof EvidenceSourceSpan
     */
    start_line: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSourceSpan
     */
    end_line: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSourceSpan
     */
    start_byte: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSourceSpan
     */
    end_byte: number;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof EvidenceSourceSpan
     */
    selector: { [key: string]: JSONValue; };
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    excerpt_hash: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    parser_version: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    schema_version: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceSpan
     */
    excerpt: string;
    /**
     *
     * @type {boolean}
     * @memberof EvidenceSourceSpan
     */
    excerpt_truncated: boolean;
}
/**
 *
 * @export
 * @interface EvidenceSourceVersion
 */
export interface EvidenceSourceVersion {
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    source_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    source_type: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    logical_name: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    relative_path: string;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    content_hash: string;
    /**
     *
     * @type {number}
     * @memberof EvidenceSourceVersion
     */
    byte_size: number;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    media_type: string;
    /**
     *
     * @type {EvidenceSourceVersionSecurityStatusEnum}
     * @memberof EvidenceSourceVersion
     */
    security_status: EvidenceSourceVersionSecurityStatusEnum;
    /**
     *
     * @type {string}
     * @memberof EvidenceSourceVersion
     */
    captured_at: string;
    /**
     *
     * @type {EvidenceSourceVersionIngestionStatusEnum}
     * @memberof EvidenceSourceVersion
     */
    ingestion_status?: EvidenceSourceVersionIngestionStatusEnum;
    /**
     *
     * @type {EvidenceSourceVersionWorkflowStatusEnum}
     * @memberof EvidenceSourceVersion
     */
    workflow_status?: EvidenceSourceVersionWorkflowStatusEnum;
    /**
     * Current Active Index selection for this Source. included binds this Source Version; excluded is Source-scoped.
     * @type {EvidenceSourceVersionIndexStatusEnum}
     * @memberof EvidenceSourceVersion
     */
    index_status?: EvidenceSourceVersionIndexStatusEnum;
}


/**
 * @export
 */
export const EvidenceSourceVersionSecurityStatusEnum = {
    Pending: 'pending',
    Passed: 'passed',
    Quarantined: 'quarantined'
} as const;
export type EvidenceSourceVersionSecurityStatusEnum = typeof EvidenceSourceVersionSecurityStatusEnum[keyof typeof EvidenceSourceVersionSecurityStatusEnum];

/**
 * @export
 */
export const EvidenceSourceVersionIngestionStatusEnum = {
    Validating: 'validating',
    Parsing: 'parsing',
    Parsed: 'parsed',
    Chunking: 'chunking',
    Chunked: 'chunked',
    ParseFailed: 'parse_failed',
    Cancelled: 'cancelled'
} as const;
export type EvidenceSourceVersionIngestionStatusEnum = typeof EvidenceSourceVersionIngestionStatusEnum[keyof typeof EvidenceSourceVersionIngestionStatusEnum];

/**
 * @export
 */
export const EvidenceSourceVersionWorkflowStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type EvidenceSourceVersionWorkflowStatusEnum = typeof EvidenceSourceVersionWorkflowStatusEnum[keyof typeof EvidenceSourceVersionWorkflowStatusEnum];

/**
 * @export
 */
export const EvidenceSourceVersionIndexStatusEnum = {
    Included: 'included',
    Excluded: 'excluded'
} as const;
export type EvidenceSourceVersionIndexStatusEnum = typeof EvidenceSourceVersionIndexStatusEnum[keyof typeof EvidenceSourceVersionIndexStatusEnum];

/**
 *
 * @export
 * @interface EvidenceSpan
 */
export interface EvidenceSpan {
    /**
     *
     * @type {string}
     * @memberof EvidenceSpan
     */
    span_id: string;
    /**
     *
     * @type {number}
     * @memberof EvidenceSpan
     */
    start_line: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSpan
     */
    end_line: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSpan
     */
    start_byte: number;
    /**
     *
     * @type {number}
     * @memberof EvidenceSpan
     */
    end_byte: number;
}
/**
 *
 * @export
 * @interface ExportCreateRequest
 */
export interface ExportCreateRequest {
    /**
     *
     * @type {string}
     * @memberof ExportCreateRequest
     */
    workspace_id: string;
    /**
     *
     * @type {ExportCreateRequestKindEnum}
     * @memberof ExportCreateRequest
     */
    kind: ExportCreateRequestKindEnum;
    /**
     *
     * @type {string}
     * @memberof ExportCreateRequest
     */
    collection_id: string;
    /**
     *
     * @type {number}
     * @memberof ExportCreateRequest
     */
    collection_version: number;
    /**
     *
     * @type {string}
     * @memberof ExportCreateRequest
     */
    query_hash: string;
    /**
     *
     * @type {Array<ExportCreateRequestFieldsEnum>}
     * @memberof ExportCreateRequest
     */
    fields?: Array<ExportCreateRequestFieldsEnum>;
    /**
     *
     * @type {ExportCreateRequestRedactionPolicyEnum}
     * @memberof ExportCreateRequest
     */
    redaction_policy?: ExportCreateRequestRedactionPolicyEnum;
    /**
     *
     * @type {boolean}
     * @memberof ExportCreateRequest
     */
    include_sensitive?: boolean;
    /**
     *
     * @type {number}
     * @memberof ExportCreateRequest
     */
    expires_in_seconds?: number;
}


/**
 * @export
 */
export const ExportCreateRequestKindEnum = {
    Markdown: 'MARKDOWN',
    MetadataJson: 'METADATA_JSON'
} as const;
export type ExportCreateRequestKindEnum = typeof ExportCreateRequestKindEnum[keyof typeof ExportCreateRequestKindEnum];

/**
 * @export
 */
export const ExportCreateRequestFieldsEnum = {
    ObjectType: 'object_type',
    Id: 'id',
    Title: 'title',
    Summary: 'summary',
    Status: 'status',
    Topic: 'topic',
    Source: 'source',
    Relations: 'relations',
    Health: 'health',
    Confidence: 'confidence',
    CreatedAt: 'created_at',
    UpdatedAt: 'updated_at',
    Applicability: 'applicability'
} as const;
export type ExportCreateRequestFieldsEnum = typeof ExportCreateRequestFieldsEnum[keyof typeof ExportCreateRequestFieldsEnum];

/**
 * @export
 */
export const ExportCreateRequestRedactionPolicyEnum = {
    Masked: 'MASKED',
    Full: 'FULL'
} as const;
export type ExportCreateRequestRedactionPolicyEnum = typeof ExportCreateRequestRedactionPolicyEnum[keyof typeof ExportCreateRequestRedactionPolicyEnum];

/**
 *
 * @export
 * @interface ExportCreateResponse
 */
export interface ExportCreateResponse {
    /**
     *
     * @type {ExportJob}
     * @memberof ExportCreateResponse
     */
    job: ExportJob;
    /**
     *
     * @type {boolean}
     * @memberof ExportCreateResponse
     */
    replayed: boolean;
    /**
     * True only when the durable PENDING job exists but immediate River dispatch was not confirmed.
     * @type {boolean}
     * @memberof ExportCreateResponse
     */
    dispatch_pending: boolean;
}
/**
 * @type ExportJob
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ExportJob = ExportJobOneOf | ExportJobOneOf1 | ExportJobOneOf2 | ExportJobOneOf3 | ExportJobOneOf4 | ExportJobOneOf5;

/**
 *
 * @export
 * @interface ExportJobOneOf
 */
export interface ExportJobOneOf {
    /**
     *
     * @type {ExportJobOneOfStatusEnum}
     * @memberof ExportJobOneOf
     */
    status?: ExportJobOneOfStatusEnum;
    /**
     *
     * @type {ExportJobOneOfFileSizeEnum}
     * @memberof ExportJobOneOf
     */
    file_size?: ExportJobOneOfFileSizeEnum;
    /**
     *
     * @type {ExportJobOneOfDownloadCountEnum}
     * @memberof ExportJobOneOf
     */
    download_count?: ExportJobOneOfDownloadCountEnum;
}


/**
 * @export
 */
export const ExportJobOneOfStatusEnum = {
    Pending: 'PENDING'
} as const;
export type ExportJobOneOfStatusEnum = typeof ExportJobOneOfStatusEnum[keyof typeof ExportJobOneOfStatusEnum];

/**
 * @export
 */
export const ExportJobOneOfFileSizeEnum = {
    NUMBER_0: 0
} as const;
export type ExportJobOneOfFileSizeEnum = typeof ExportJobOneOfFileSizeEnum[keyof typeof ExportJobOneOfFileSizeEnum];

/**
 * @export
 */
export const ExportJobOneOfDownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type ExportJobOneOfDownloadCountEnum = typeof ExportJobOneOfDownloadCountEnum[keyof typeof ExportJobOneOfDownloadCountEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf1
 */
export interface ExportJobOneOf1 {
    /**
     *
     * @type {ExportJobOneOf1StatusEnum}
     * @memberof ExportJobOneOf1
     */
    status?: ExportJobOneOf1StatusEnum;
    /**
     *
     * @type {ExportJobOneOf1DownloadCountEnum}
     * @memberof ExportJobOneOf1
     */
    download_count?: ExportJobOneOf1DownloadCountEnum;
}


/**
 * @export
 */
export const ExportJobOneOf1StatusEnum = {
    Running: 'RUNNING'
} as const;
export type ExportJobOneOf1StatusEnum = typeof ExportJobOneOf1StatusEnum[keyof typeof ExportJobOneOf1StatusEnum];

/**
 * @export
 */
export const ExportJobOneOf1DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type ExportJobOneOf1DownloadCountEnum = typeof ExportJobOneOf1DownloadCountEnum[keyof typeof ExportJobOneOf1DownloadCountEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf1Not
 */
export interface ExportJobOneOf1Not {
}
/**
 *
 * @export
 * @interface ExportJobOneOf2
 */
export interface ExportJobOneOf2 {
    /**
     *
     * @type {ExportJobOneOf2StatusEnum}
     * @memberof ExportJobOneOf2
     */
    status?: ExportJobOneOf2StatusEnum;
}


/**
 * @export
 */
export const ExportJobOneOf2StatusEnum = {
    Succeeded: 'SUCCEEDED'
} as const;
export type ExportJobOneOf2StatusEnum = typeof ExportJobOneOf2StatusEnum[keyof typeof ExportJobOneOf2StatusEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf2Not
 */
export interface ExportJobOneOf2Not {
}
/**
 *
 * @export
 * @interface ExportJobOneOf3
 */
export interface ExportJobOneOf3 {
    /**
     *
     * @type {ExportJobOneOf3StatusEnum}
     * @memberof ExportJobOneOf3
     */
    status?: ExportJobOneOf3StatusEnum;
    /**
     *
     * @type {ExportJobOneOf3DownloadCountEnum}
     * @memberof ExportJobOneOf3
     */
    download_count?: ExportJobOneOf3DownloadCountEnum;
}


/**
 * @export
 */
export const ExportJobOneOf3StatusEnum = {
    Failed: 'FAILED'
} as const;
export type ExportJobOneOf3StatusEnum = typeof ExportJobOneOf3StatusEnum[keyof typeof ExportJobOneOf3StatusEnum];

/**
 * @export
 */
export const ExportJobOneOf3DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type ExportJobOneOf3DownloadCountEnum = typeof ExportJobOneOf3DownloadCountEnum[keyof typeof ExportJobOneOf3DownloadCountEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf3Not
 */
export interface ExportJobOneOf3Not {
}
/**
 *
 * @export
 * @interface ExportJobOneOf4
 */
export interface ExportJobOneOf4 {
    /**
     *
     * @type {ExportJobOneOf4StatusEnum}
     * @memberof ExportJobOneOf4
     */
    status?: ExportJobOneOf4StatusEnum;
}


/**
 * @export
 */
export const ExportJobOneOf4StatusEnum = {
    Expired: 'EXPIRED'
} as const;
export type ExportJobOneOf4StatusEnum = typeof ExportJobOneOf4StatusEnum[keyof typeof ExportJobOneOf4StatusEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf4Not
 */
export interface ExportJobOneOf4Not {
}
/**
 *
 * @export
 * @interface ExportJobOneOf5
 */
export interface ExportJobOneOf5 {
    /**
     *
     * @type {ExportJobOneOf5StatusEnum}
     * @memberof ExportJobOneOf5
     */
    status?: ExportJobOneOf5StatusEnum;
    /**
     *
     * @type {ExportJobOneOf5DownloadCountEnum}
     * @memberof ExportJobOneOf5
     */
    download_count?: ExportJobOneOf5DownloadCountEnum;
}


/**
 * @export
 */
export const ExportJobOneOf5StatusEnum = {
    Cancelled: 'CANCELLED'
} as const;
export type ExportJobOneOf5StatusEnum = typeof ExportJobOneOf5StatusEnum[keyof typeof ExportJobOneOf5StatusEnum];

/**
 * @export
 */
export const ExportJobOneOf5DownloadCountEnum = {
    NUMBER_0: 0
} as const;
export type ExportJobOneOf5DownloadCountEnum = typeof ExportJobOneOf5DownloadCountEnum[keyof typeof ExportJobOneOf5DownloadCountEnum];

/**
 *
 * @export
 * @interface ExportJobOneOf5Not
 */
export interface ExportJobOneOf5Not {
}
/**
 *
 * @export
 * @interface ExportJobOneOfNot
 */
export interface ExportJobOneOfNot {
}
/**
 *
 * @export
 * @interface ExportPage
 */
export interface ExportPage {
    /**
     *
     * @type {string}
     * @memberof ExportPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<ExportJob>}
     * @memberof ExportPage
     */
    items: Array<ExportJob>;
    /**
     *
     * @type {string}
     * @memberof ExportPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface FilePatchProposal
 */
export interface FilePatchProposal {
    /**
     *
     * @type {FilePatchProposalProposalTypeEnum}
     * @memberof FilePatchProposal
     */
    proposal_type: FilePatchProposalProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof FilePatchProposal
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof FilePatchProposal
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof FilePatchProposal
     */
    target_path: string;
    /**
     *
     * @type {FilePatchProposalStatusEnum}
     * @memberof FilePatchProposal
     */
    status: FilePatchProposalStatusEnum;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof FilePatchProposal
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {number}
     * @memberof FilePatchProposal
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof FilePatchProposal
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {ProposalRevision}
     * @memberof FilePatchProposal
     */
    revision: ProposalRevision;
    /**
     *
     * @type {Approval}
     * @memberof FilePatchProposal
     */
    approval: Approval | null;
    /**
     *
     * @type {string}
     * @memberof FilePatchProposal
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof FilePatchProposal
     */
    updated_at: string;
}


/**
 * @export
 */
export const FilePatchProposalProposalTypeEnum = {
    FilePatch: 'file_patch'
} as const;
export type FilePatchProposalProposalTypeEnum = typeof FilePatchProposalProposalTypeEnum[keyof typeof FilePatchProposalProposalTypeEnum];

/**
 * @export
 */
export const FilePatchProposalStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type FilePatchProposalStatusEnum = typeof FilePatchProposalStatusEnum[keyof typeof FilePatchProposalStatusEnum];

/**
 *
 * @export
 * @interface FreezeWorkingDraftRequest
 */
export interface FreezeWorkingDraftRequest {
    /**
     *
     * @type {number}
     * @memberof FreezeWorkingDraftRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface FreezeWorkingDraftResult
 */
export interface FreezeWorkingDraftResult {
    /**
     *
     * @type {WorkingDraft}
     * @memberof FreezeWorkingDraftResult
     */
    working_draft: WorkingDraft;
    /**
     *
     * @type {DocumentDraft}
     * @memberof FreezeWorkingDraftResult
     */
    document: DocumentDraft;
    /**
     *
     * @type {ArticleRevision}
     * @memberof FreezeWorkingDraftResult
     */
    article_revision: ArticleRevision;
    /**
     *
     * @type {boolean}
     * @memberof FreezeWorkingDraftResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface GitRemoteConfig
 */
export interface GitRemoteConfig {
    /**
     *
     * @type {string}
     * @memberof GitRemoteConfig
     */
    workspace_id: string;
    /**
     *
     * @type {boolean}
     * @memberof GitRemoteConfig
     */
    configured: boolean;
    /**
     * Canonical public HTTPS repository URL without userinfo, query, or fragment.
     * @type {string}
     * @memberof GitRemoteConfig
     */
    remote_url: string | null;
    /**
     *
     * @type {string}
     * @memberof GitRemoteConfig
     */
    branch: string | null;
    /**
     *
     * @type {boolean}
     * @memberof GitRemoteConfig
     */
    auto_sync: boolean;
    /**
     * Whether a credential is present; the credential itself is never returned.
     * @type {boolean}
     * @memberof GitRemoteConfig
     */
    token_configured: boolean;
    /**
     *
     * @type {number}
     * @memberof GitRemoteConfig
     */
    revision: number;
    /**
     *
     * @type {string}
     * @memberof GitRemoteConfig
     */
    created_at: string | null;
    /**
     *
     * @type {string}
     * @memberof GitRemoteConfig
     */
    updated_at: string | null;
    /**
     *
     * @type {boolean}
     * @memberof GitRemoteConfig
     */
    replayed?: boolean;
}
/**
 *
 * @export
 * @interface GitRemoteTestResult
 */
export interface GitRemoteTestResult {
    /**
     *
     * @type {GitRemoteTestResultStatusEnum}
     * @memberof GitRemoteTestResult
     */
    status: GitRemoteTestResultStatusEnum;
    /**
     * Canonical public HTTPS repository URL without userinfo, query, or fragment.
     * @type {string}
     * @memberof GitRemoteTestResult
     */
    remote_url: string;
    /**
     *
     * @type {string}
     * @memberof GitRemoteTestResult
     */
    branch: string;
}


/**
 * @export
 */
export const GitRemoteTestResultStatusEnum = {
    Ok: 'ok'
} as const;
export type GitRemoteTestResultStatusEnum = typeof GitRemoteTestResultStatusEnum[keyof typeof GitRemoteTestResultStatusEnum];

/**
 *
 * @export
 * @interface GitStatus
 */
export interface GitStatus {
    /**
     *
     * @type {boolean}
     * @memberof GitStatus
     */
    present: boolean;
    /**
     *
     * @type {string}
     * @memberof GitStatus
     */
    repository_path: string;
    /**
     *
     * @type {string}
     * @memberof GitStatus
     */
    branch: string;
    /**
     *
     * @type {string}
     * @memberof GitStatus
     */
    head: string;
    /**
     *
     * @type {boolean}
     * @memberof GitStatus
     */
    dirty: boolean;
    /**
     *
     * @type {string}
     * @memberof GitStatus
     */
    checked_at: string;
}
/**
 *
 * @export
 * @interface GitSyncClearTokenAction
 */
export interface GitSyncClearTokenAction {
    /**
     *
     * @type {GitSyncClearTokenActionActionEnum}
     * @memberof GitSyncClearTokenAction
     */
    action: GitSyncClearTokenActionActionEnum;
}


/**
 * @export
 */
export const GitSyncClearTokenActionActionEnum = {
    Clear: 'clear'
} as const;
export type GitSyncClearTokenActionActionEnum = typeof GitSyncClearTokenActionActionEnum[keyof typeof GitSyncClearTokenActionActionEnum];

/**
 *
 * @export
 * @interface GitSyncFileChange
 */
export interface GitSyncFileChange {
    /**
     *
     * @type {string}
     * @memberof GitSyncFileChange
     */
    path: string;
    /**
     *
     * @type {string}
     * @memberof GitSyncFileChange
     */
    old_path: string | null;
    /**
     *
     * @type {GitSyncFileChangeKindEnum}
     * @memberof GitSyncFileChange
     */
    kind: GitSyncFileChangeKindEnum;
}


/**
 * @export
 */
export const GitSyncFileChangeKindEnum = {
    Added: 'ADDED',
    Modified: 'MODIFIED',
    Deleted: 'DELETED',
    Renamed: 'RENAMED'
} as const;
export type GitSyncFileChangeKindEnum = typeof GitSyncFileChangeKindEnum[keyof typeof GitSyncFileChangeKindEnum];

/**
 *
 * @export
 * @interface GitSyncKeepTokenAction
 */
export interface GitSyncKeepTokenAction {
    /**
     *
     * @type {GitSyncKeepTokenActionActionEnum}
     * @memberof GitSyncKeepTokenAction
     */
    action: GitSyncKeepTokenActionActionEnum;
}


/**
 * @export
 */
export const GitSyncKeepTokenActionActionEnum = {
    Keep: 'keep'
} as const;
export type GitSyncKeepTokenActionActionEnum = typeof GitSyncKeepTokenActionActionEnum[keyof typeof GitSyncKeepTokenActionActionEnum];

/**
 *
 * @export
 * @interface GitSyncReplaceTokenAction
 */
export interface GitSyncReplaceTokenAction {
    /**
     *
     * @type {GitSyncReplaceTokenActionActionEnum}
     * @memberof GitSyncReplaceTokenAction
     */
    action: GitSyncReplaceTokenActionActionEnum;
    /**
     *
     * @type {string}
     * @memberof GitSyncReplaceTokenAction
     */
    value: string;
}


/**
 * @export
 */
export const GitSyncReplaceTokenActionActionEnum = {
    Replace: 'replace'
} as const;
export type GitSyncReplaceTokenActionActionEnum = typeof GitSyncReplaceTokenActionActionEnum[keyof typeof GitSyncReplaceTokenActionActionEnum];

/**
 *
 * @export
 * @interface GitSyncRun
 */
export interface GitSyncRun {
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof GitSyncRun
     */
    config_revision: number;
    /**
     * Canonical public HTTPS repository URL without userinfo, query, or fragment.
     * @type {string}
     * @memberof GitSyncRun
     */
    remote_url: string;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    branch: string;
    /**
     *
     * @type {GitSyncRunTriggerEnum}
     * @memberof GitSyncRun
     */
    trigger: GitSyncRunTriggerEnum;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    retry_of_run_id: string | null;
    /**
     *
     * @type {GitSyncRunStatusEnum}
     * @memberof GitSyncRun
     */
    status: GitSyncRunStatusEnum;
    /**
     *
     * @type {GitSyncRunDirectionEnum}
     * @memberof GitSyncRun
     */
    direction: GitSyncRunDirectionEnum;
    /**
     *
     * @type {GitSyncRunFailureClassEnum}
     * @memberof GitSyncRun
     */
    failure_class: GitSyncRunFailureClassEnum;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    error_code: string;
    /**
     *
     * @type {boolean}
     * @memberof GitSyncRun
     */
    retryable: boolean;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    expected_head_oid: string | null;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    expected_remote_oid: string | null;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    verified_head_oid: string | null;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    verified_remote_oid: string | null;
    /**
     *
     * @type {Array<GitSyncFileChange>}
     * @memberof GitSyncRun
     */
    changed_files: Array<GitSyncFileChange>;
    /**
     *
     * @type {GitSyncRunIndexStatusEnum}
     * @memberof GitSyncRun
     */
    index_status: GitSyncRunIndexStatusEnum;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    index_error_code: string;
    /**
     *
     * @type {boolean}
     * @memberof GitSyncRun
     */
    index_retryable: boolean;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    index_version_id: string | null;
    /**
     *
     * @type {number}
     * @memberof GitSyncRun
     */
    attempt_count: number;
    /**
     *
     * @type {number}
     * @memberof GitSyncRun
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof GitSyncRun
     */
    completed_at: string | null;
    /**
     *
     * @type {boolean}
     * @memberof GitSyncRun
     */
    replayed?: boolean;
}


/**
 * @export
 */
export const GitSyncRunTriggerEnum = {
    Manual: 'MANUAL',
    Automatic: 'AUTOMATIC',
    Retry: 'RETRY'
} as const;
export type GitSyncRunTriggerEnum = typeof GitSyncRunTriggerEnum[keyof typeof GitSyncRunTriggerEnum];

/**
 * @export
 */
export const GitSyncRunStatusEnum = {
    Pending: 'PENDING',
    Fetching: 'FETCHING',
    Comparing: 'COMPARING',
    FastForwarding: 'FAST_FORWARDING',
    Pushing: 'PUSHING',
    Verifying: 'VERIFYING',
    Succeeded: 'SUCCEEDED',
    Conflict: 'CONFLICT',
    Failed: 'FAILED',
    Stale: 'STALE',
    ManualRecoveryRequired: 'MANUAL_RECOVERY_REQUIRED'
} as const;
export type GitSyncRunStatusEnum = typeof GitSyncRunStatusEnum[keyof typeof GitSyncRunStatusEnum];

/**
 * @export
 */
export const GitSyncRunDirectionEnum = {
    Unknown: 'UNKNOWN',
    None: 'NONE',
    Pull: 'PULL',
    Push: 'PUSH'
} as const;
export type GitSyncRunDirectionEnum = typeof GitSyncRunDirectionEnum[keyof typeof GitSyncRunDirectionEnum];

/**
 * @export
 */
export const GitSyncRunFailureClassEnum = {
    None: 'NONE',
    Dirty: 'DIRTY',
    Detached: 'DETACHED',
    Diverged: 'DIVERGED',
    Authentication: 'AUTHENTICATION',
    Offline: 'OFFLINE',
    RefDrift: 'REF_DRIFT',
    NonFastForward: 'NON_FAST_FORWARD',
    StaleConfig: 'STALE_CONFIG',
    ResultUnknown: 'RESULT_UNKNOWN',
    Dependency: 'DEPENDENCY',
    Internal: 'INTERNAL'
} as const;
export type GitSyncRunFailureClassEnum = typeof GitSyncRunFailureClassEnum[keyof typeof GitSyncRunFailureClassEnum];

/**
 * @export
 */
export const GitSyncRunIndexStatusEnum = {
    NotRequired: 'NOT_REQUIRED',
    Pending: 'PENDING',
    Running: 'RUNNING',
    Succeeded: 'SUCCEEDED',
    Failed: 'FAILED'
} as const;
export type GitSyncRunIndexStatusEnum = typeof GitSyncRunIndexStatusEnum[keyof typeof GitSyncRunIndexStatusEnum];

/**
 *
 * @export
 * @interface GitSyncRunPage
 */
export interface GitSyncRunPage {
    /**
     *
     * @type {Array<GitSyncRun>}
     * @memberof GitSyncRunPage
     */
    items: Array<GitSyncRun>;
    /**
     *
     * @type {string}
     * @memberof GitSyncRunPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface GitSyncStatus
 */
export interface GitSyncStatus {
    /**
     *
     * @type {GitRemoteConfig}
     * @memberof GitSyncStatus
     */
    config: GitRemoteConfig;
    /**
     *
     * @type {GitSyncRun}
     * @memberof GitSyncStatus
     */
    current_run: GitSyncRun | null;
}
/**
 * @type GitSyncTestTokenAction
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type GitSyncTestTokenAction = GitSyncKeepTokenAction | GitSyncReplaceTokenAction;

/**
 * @type GitSyncTokenAction
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type GitSyncTokenAction = GitSyncClearTokenAction | GitSyncKeepTokenAction | GitSyncReplaceTokenAction;

/**
 *
 * @export
 * @interface GraphApplicability
 */
export interface GraphApplicability {
    /**
     *
     * @type {GraphApplicabilitySchemaVersionEnum}
     * @memberof GraphApplicability
     */
    schema_version: GraphApplicabilitySchemaVersionEnum;
    /**
     * Canonical applicability JSON object. Arbitrary keys are allowed, but the server rejects duplicate keys, invalid Unicode, excessive depth and oversized payloads.
     * @type {object}
     * @memberof GraphApplicability
     */
    value: object;
    /**
     *
     * @type {string}
     * @memberof GraphApplicability
     */
    hash: string;
}


/**
 * @export
 */
export const GraphApplicabilitySchemaVersionEnum = {
    KnowledgeApplicabilityV1: 'knowledge-applicability/v1'
} as const;
export type GraphApplicabilitySchemaVersionEnum = typeof GraphApplicabilitySchemaVersionEnum[keyof typeof GraphApplicabilitySchemaVersionEnum];

/**
 * @type GraphCanonicalJSONValue
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type GraphCanonicalJSONValue = Array<GraphCanonicalJSONValue> | boolean | number | object | string | null;

/**
 *
 * @export
 * @interface GraphCapabilityStatus
 */
export interface GraphCapabilityStatus {
    /**
     *
     * @type {GraphCapabilityStatusStatusEnum}
     * @memberof GraphCapabilityStatus
     */
    status: GraphCapabilityStatusStatusEnum;
    /**
     *
     * @type {GraphCapabilityStatusReasonEnum}
     * @memberof GraphCapabilityStatus
     */
    reason?: GraphCapabilityStatusReasonEnum;
}


/**
 * @export
 */
export const GraphCapabilityStatusStatusEnum = {
    Ready: 'ready',
    Unavailable: 'unavailable'
} as const;
export type GraphCapabilityStatusStatusEnum = typeof GraphCapabilityStatusStatusEnum[keyof typeof GraphCapabilityStatusStatusEnum];

/**
 * @export
 */
export const GraphCapabilityStatusReasonEnum = {
    GraphDependenciesUnavailable: 'graph_dependencies_unavailable'
} as const;
export type GraphCapabilityStatusReasonEnum = typeof GraphCapabilityStatusReasonEnum[keyof typeof GraphCapabilityStatusReasonEnum];

/**
 *
 * @export
 * @interface GraphClaimNode
 */
export interface GraphClaimNode {
    /**
     *
     * @type {GraphClaimNodeTypeEnum}
     * @memberof GraphClaimNode
     */
    type: GraphClaimNodeTypeEnum;
    /**
     *
     * @type {string}
     * @memberof GraphClaimNode
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof GraphClaimNode
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphClaimNode
     */
    statement: string;
    /**
     *
     * @type {GraphClaimNodeClaimStatusEnum}
     * @memberof GraphClaimNode
     */
    claim_status: GraphClaimNodeClaimStatusEnum;
    /**
     *
     * @type {number}
     * @memberof GraphClaimNode
     */
    confidence: number | null;
    /**
     *
     * @type {GraphApplicability}
     * @memberof GraphClaimNode
     */
    applicability: GraphApplicability;
    /**
     *
     * @type {number}
     * @memberof GraphClaimNode
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof GraphClaimNode
     */
    updated_at: string;
}


/**
 * @export
 */
export const GraphClaimNodeTypeEnum = {
    Claim: 'CLAIM'
} as const;
export type GraphClaimNodeTypeEnum = typeof GraphClaimNodeTypeEnum[keyof typeof GraphClaimNodeTypeEnum];

/**
 * @export
 */
export const GraphClaimNodeClaimStatusEnum = {
    Suggested: 'SUGGESTED',
    Confirmed: 'CONFIRMED',
    Disputed: 'DISPUTED',
    Superseded: 'SUPERSEDED',
    Deprecated: 'DEPRECATED',
    Invalid: 'INVALID'
} as const;
export type GraphClaimNodeClaimStatusEnum = typeof GraphClaimNodeClaimStatusEnum[keyof typeof GraphClaimNodeClaimStatusEnum];

/**
 *
 * @export
 * @interface GraphConfirmation
 */
export interface GraphConfirmation {
    /**
     *
     * @type {GraphConfirmationMethodEnum}
     * @memberof GraphConfirmation
     */
    method: GraphConfirmationMethodEnum;
    /**
     *
     * @type {string}
     * @memberof GraphConfirmation
     */
    reference: string;
}


/**
 * @export
 */
export const GraphConfirmationMethodEnum = {
    UserApproval: 'USER_APPROVAL',
    SourceDerived: 'SOURCE_DERIVED'
} as const;
export type GraphConfirmationMethodEnum = typeof GraphConfirmationMethodEnum[keyof typeof GraphConfirmationMethodEnum];

/**
 *
 * @export
 * @interface GraphEdge
 */
export interface GraphEdge {
    /**
     *
     * @type {string}
     * @memberof GraphEdge
     */
    relation_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphEdge
     */
    workspace_id: string;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphEdge
     */
    source: GraphNodeRef;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphEdge
     */
    target: GraphNodeRef;
    /**
     *
     * @type {GraphEdgeTypeEnum}
     * @memberof GraphEdge
     */
    type: GraphEdgeTypeEnum;
    /**
     *
     * @type {GraphEdgeStatusEnum}
     * @memberof GraphEdge
     */
    status: GraphEdgeStatusEnum;
    /**
     *
     * @type {GraphEdgeTraversalEnum}
     * @memberof GraphEdge
     */
    traversal: GraphEdgeTraversalEnum;
    /**
     *
     * @type {number}
     * @memberof GraphEdge
     */
    confidence: number | null;
    /**
     *
     * @type {number}
     * @memberof GraphEdge
     */
    version: number;
    /**
     *
     * @type {number}
     * @memberof GraphEdge
     */
    evidence_count: number;
    /**
     *
     * @type {string}
     * @memberof GraphEdge
     */
    evidence_fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof GraphEdge
     */
    evidence_href: string;
    /**
     *
     * @type {string}
     * @memberof GraphEdge
     */
    updated_at: string;
}


/**
 * @export
 */
export const GraphEdgeTypeEnum = {
    Cites: 'CITES',
    DerivedFrom: 'DERIVED_FROM',
    BelongsTo: 'BELONGS_TO',
    Supports: 'SUPPORTS',
    Complements: 'COMPLEMENTS',
    Duplicates: 'DUPLICATES',
    ConflictsWith: 'CONFLICTS_WITH',
    PrerequisiteOf: 'PREREQUISITE_OF',
    VersionOf: 'VERSION_OF',
    Impacts: 'IMPACTS'
} as const;
export type GraphEdgeTypeEnum = typeof GraphEdgeTypeEnum[keyof typeof GraphEdgeTypeEnum];

/**
 * @export
 */
export const GraphEdgeStatusEnum = {
    Confirmed: 'CONFIRMED',
    Stale: 'STALE'
} as const;
export type GraphEdgeStatusEnum = typeof GraphEdgeStatusEnum[keyof typeof GraphEdgeStatusEnum];

/**
 * @export
 */
export const GraphEdgeTraversalEnum = {
    Forward: 'FORWARD',
    Reverse: 'REVERSE'
} as const;
export type GraphEdgeTraversalEnum = typeof GraphEdgeTraversalEnum[keyof typeof GraphEdgeTraversalEnum];

/**
 *
 * @export
 * @interface GraphFilter
 */
export interface GraphFilter {
    /**
     *
     * @type {Array<GraphFilterNodeTypesEnum>}
     * @memberof GraphFilter
     */
    node_types?: Array<GraphFilterNodeTypesEnum>;
    /**
     *
     * @type {Array<GraphFilterRelationTypesEnum>}
     * @memberof GraphFilter
     */
    relation_types?: Array<GraphFilterRelationTypesEnum>;
    /**
     *
     * @type {Array<string>}
     * @memberof GraphFilter
     */
    topic_ids?: Array<string>;
    /**
     *
     * @type {Array<GraphFilterRelationStatusesEnum>}
     * @memberof GraphFilter
     */
    relation_statuses?: Array<GraphFilterRelationStatusesEnum>;
    /**
     *
     * @type {Array<GraphFilterClaimStatusesEnum>}
     * @memberof GraphFilter
     */
    claim_statuses?: Array<GraphFilterClaimStatusesEnum>;
    /**
     *
     * @type {number}
     * @memberof GraphFilter
     */
    claim_min_confidence?: number;
    /**
     *
     * @type {number}
     * @memberof GraphFilter
     */
    relation_min_confidence?: number;
    /**
     *
     * @type {string}
     * @memberof GraphFilter
     */
    updated_after?: string;
}


/**
 * @export
 */
export const GraphFilterNodeTypesEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM'
} as const;
export type GraphFilterNodeTypesEnum = typeof GraphFilterNodeTypesEnum[keyof typeof GraphFilterNodeTypesEnum];

/**
 * @export
 */
export const GraphFilterRelationTypesEnum = {
    Cites: 'CITES',
    DerivedFrom: 'DERIVED_FROM',
    BelongsTo: 'BELONGS_TO',
    Supports: 'SUPPORTS',
    Complements: 'COMPLEMENTS',
    Duplicates: 'DUPLICATES',
    ConflictsWith: 'CONFLICTS_WITH',
    PrerequisiteOf: 'PREREQUISITE_OF',
    VersionOf: 'VERSION_OF',
    Impacts: 'IMPACTS'
} as const;
export type GraphFilterRelationTypesEnum = typeof GraphFilterRelationTypesEnum[keyof typeof GraphFilterRelationTypesEnum];

/**
 * @export
 */
export const GraphFilterRelationStatusesEnum = {
    Confirmed: 'CONFIRMED',
    Stale: 'STALE'
} as const;
export type GraphFilterRelationStatusesEnum = typeof GraphFilterRelationStatusesEnum[keyof typeof GraphFilterRelationStatusesEnum];

/**
 * @export
 */
export const GraphFilterClaimStatusesEnum = {
    Confirmed: 'CONFIRMED',
    Disputed: 'DISPUTED'
} as const;
export type GraphFilterClaimStatusesEnum = typeof GraphFilterClaimStatusesEnum[keyof typeof GraphFilterClaimStatusesEnum];

/**
 *
 * @export
 * @interface GraphGlobalCluster
 */
export interface GraphGlobalCluster {
    /**
     *
     * @type {GraphTopicNode}
     * @memberof GraphGlobalCluster
     */
    topic: GraphTopicNode;
    /**
     *
     * @type {number}
     * @memberof GraphGlobalCluster
     */
    direct_claim_count: number;
    /**
     *
     * @type {number}
     * @memberof GraphGlobalCluster
     */
    incident_relation_count: number;
    /**
     *
     * @type {number}
     * @memberof GraphGlobalCluster
     */
    cluster_score: number;
    /**
     *
     * @type {string}
     * @memberof GraphGlobalCluster
     */
    updated_at: string;
}
/**
 *
 * @export
 * @interface GraphGlobalRequest
 */
export interface GraphGlobalRequest {
    /**
     *
     * @type {string}
     * @memberof GraphGlobalRequest
     */
    workspace_id: string;
    /**
     *
     * @type {GraphFilter}
     * @memberof GraphGlobalRequest
     */
    filter?: GraphFilter;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof GraphGlobalRequest
     */
    cursor?: string;
    /**
     *
     * @type {number}
     * @memberof GraphGlobalRequest
     */
    limit?: number;
}
/**
 *
 * @export
 * @interface GraphGlobalResponse
 */
export interface GraphGlobalResponse {
    /**
     *
     * @type {string}
     * @memberof GraphGlobalResponse
     */
    workspace_id: string;
    /**
     *
     * @type {Array<GraphGlobalCluster>}
     * @memberof GraphGlobalResponse
     */
    clusters: Array<GraphGlobalCluster>;
    /**
     *
     * @type {GraphPageMeta}
     * @memberof GraphGlobalResponse
     */
    meta: GraphPageMeta;
}
/**
 *
 * @export
 * @interface GraphNeighborhoodRequest
 */
export interface GraphNeighborhoodRequest {
    /**
     *
     * @type {string}
     * @memberof GraphNeighborhoodRequest
     */
    workspace_id: string;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphNeighborhoodRequest
     */
    center: GraphNodeRef;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodRequest
     */
    depth?: number;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodRequest
     */
    limit?: number;
    /**
     *
     * @type {GraphNeighborhoodRequestDirectionEnum}
     * @memberof GraphNeighborhoodRequest
     */
    direction?: GraphNeighborhoodRequestDirectionEnum;
    /**
     *
     * @type {GraphFilter}
     * @memberof GraphNeighborhoodRequest
     */
    filter?: GraphFilter;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodRequest
     */
    max_nodes?: number;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodRequest
     */
    max_edges?: number;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodRequest
     */
    max_frontier?: number;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof GraphNeighborhoodRequest
     */
    cursor?: string;
}


/**
 * @export
 */
export const GraphNeighborhoodRequestDirectionEnum = {
    Both: 'BOTH',
    Outbound: 'OUTBOUND',
    Inbound: 'INBOUND'
} as const;
export type GraphNeighborhoodRequestDirectionEnum = typeof GraphNeighborhoodRequestDirectionEnum[keyof typeof GraphNeighborhoodRequestDirectionEnum];

/**
 *
 * @export
 * @interface GraphNeighborhoodResponse
 */
export interface GraphNeighborhoodResponse {
    /**
     *
     * @type {string}
     * @memberof GraphNeighborhoodResponse
     */
    workspace_id: string;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphNeighborhoodResponse
     */
    center: GraphNodeRef;
    /**
     *
     * @type {Array<GraphNode>}
     * @memberof GraphNeighborhoodResponse
     */
    nodes: Array<GraphNode>;
    /**
     *
     * @type {Array<GraphEdge>}
     * @memberof GraphNeighborhoodResponse
     */
    edges: Array<GraphEdge>;
    /**
     *
     * @type {Array<GraphNodeRef>}
     * @memberof GraphNeighborhoodResponse
     */
    boundary_nodes: Array<GraphNodeRef>;
    /**
     *
     * @type {Array<number>}
     * @memberof GraphNeighborhoodResponse
     */
    layer_counts: Array<number>;
    /**
     *
     * @type {number}
     * @memberof GraphNeighborhoodResponse
     */
    completed_depth: number;
    /**
     *
     * @type {GraphPageMeta}
     * @memberof GraphNeighborhoodResponse
     */
    meta: GraphPageMeta;
}
/**
 * @type GraphNode
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type GraphNode = { type: 'CLAIM' } & GraphClaimNode | { type: 'TOPIC' } & GraphTopicNode;

/**
 *
 * @export
 * @interface GraphNodeRef
 */
export interface GraphNodeRef {
    /**
     *
     * @type {GraphNodeRefTypeEnum}
     * @memberof GraphNodeRef
     */
    type: GraphNodeRefTypeEnum;
    /**
     *
     * @type {string}
     * @memberof GraphNodeRef
     */
    id: string;
}


/**
 * @export
 */
export const GraphNodeRefTypeEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM'
} as const;
export type GraphNodeRefTypeEnum = typeof GraphNodeRefTypeEnum[keyof typeof GraphNodeRefTypeEnum];

/**
 *
 * @export
 * @interface GraphNodeSearchMatch
 */
export interface GraphNodeSearchMatch {
    /**
     *
     * @type {GraphNodeSearchMatchKindEnum}
     * @memberof GraphNodeSearchMatch
     */
    kind: GraphNodeSearchMatchKindEnum;
    /**
     *
     * @type {GraphNode}
     * @memberof GraphNodeSearchMatch
     */
    node: GraphNode;
}


/**
 * @export
 */
export const GraphNodeSearchMatchKindEnum = {
    Exact: 'EXACT',
    Prefix: 'PREFIX'
} as const;
export type GraphNodeSearchMatchKindEnum = typeof GraphNodeSearchMatchKindEnum[keyof typeof GraphNodeSearchMatchKindEnum];

/**
 *
 * @export
 * @interface GraphNodeSearchResponse
 */
export interface GraphNodeSearchResponse {
    /**
     *
     * @type {string}
     * @memberof GraphNodeSearchResponse
     */
    workspace_id: string;
    /**
     *
     * @type {Array<GraphNodeSearchMatch>}
     * @memberof GraphNodeSearchResponse
     */
    matches: Array<GraphNodeSearchMatch>;
}
/**
 *
 * @export
 * @interface GraphPageMeta
 */
export interface GraphPageMeta {
    /**
     *
     * @type {string}
     * @memberof GraphPageMeta
     */
    fingerprint: string;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof GraphPageMeta
     */
    next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof GraphPageMeta
     */
    complete: boolean;
    /**
     *
     * @type {boolean}
     * @memberof GraphPageMeta
     */
    truncated: boolean;
    /**
     *
     * @type {string}
     * @memberof GraphPageMeta
     */
    reason?: string;
}
/**
 *
 * @export
 * @interface GraphPathRequest
 */
export interface GraphPathRequest {
    /**
     *
     * @type {string}
     * @memberof GraphPathRequest
     */
    workspace_id: string;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphPathRequest
     */
    from: GraphNodeRef;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphPathRequest
     */
    to: GraphNodeRef;
    /**
     *
     * @type {GraphPathRequestDirectionEnum}
     * @memberof GraphPathRequest
     */
    direction?: GraphPathRequestDirectionEnum;
    /**
     *
     * @type {Array<GraphPathRequestRelationTypesEnum>}
     * @memberof GraphPathRequest
     */
    relation_types?: Array<GraphPathRequestRelationTypesEnum>;
    /**
     *
     * @type {number}
     * @memberof GraphPathRequest
     */
    max_depth?: number;
    /**
     *
     * @type {number}
     * @memberof GraphPathRequest
     */
    max_visited?: number;
}


/**
 * @export
 */
export const GraphPathRequestDirectionEnum = {
    Both: 'BOTH',
    Outbound: 'OUTBOUND',
    Inbound: 'INBOUND'
} as const;
export type GraphPathRequestDirectionEnum = typeof GraphPathRequestDirectionEnum[keyof typeof GraphPathRequestDirectionEnum];

/**
 * @export
 */
export const GraphPathRequestRelationTypesEnum = {
    Cites: 'CITES',
    DerivedFrom: 'DERIVED_FROM',
    BelongsTo: 'BELONGS_TO',
    Supports: 'SUPPORTS',
    Complements: 'COMPLEMENTS',
    Duplicates: 'DUPLICATES',
    ConflictsWith: 'CONFLICTS_WITH',
    PrerequisiteOf: 'PREREQUISITE_OF',
    VersionOf: 'VERSION_OF',
    Impacts: 'IMPACTS'
} as const;
export type GraphPathRequestRelationTypesEnum = typeof GraphPathRequestRelationTypesEnum[keyof typeof GraphPathRequestRelationTypesEnum];

/**
 *
 * @export
 * @interface GraphPathResponse
 */
export interface GraphPathResponse {
    /**
     *
     * @type {string}
     * @memberof GraphPathResponse
     */
    workspace_id: string;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphPathResponse
     */
    from: GraphNodeRef;
    /**
     *
     * @type {GraphNodeRef}
     * @memberof GraphPathResponse
     */
    to: GraphNodeRef;
    /**
     *
     * @type {GraphPathResponseStatusEnum}
     * @memberof GraphPathResponse
     */
    status: GraphPathResponseStatusEnum;
    /**
     *
     * @type {Array<GraphNode>}
     * @memberof GraphPathResponse
     */
    nodes: Array<GraphNode>;
    /**
     *
     * @type {Array<GraphEdge>}
     * @memberof GraphPathResponse
     */
    edges: Array<GraphEdge>;
    /**
     *
     * @type {number}
     * @memberof GraphPathResponse
     */
    hop_count: number;
    /**
     *
     * @type {number}
     * @memberof GraphPathResponse
     */
    explored_nodes: number;
    /**
     *
     * @type {Array<GraphTopicNode>}
     * @memberof GraphPathResponse
     */
    common_topic_suggestions: Array<GraphTopicNode>;
}


/**
 * @export
 */
export const GraphPathResponseStatusEnum = {
    Found: 'found',
    NotFound: 'not_found'
} as const;
export type GraphPathResponseStatusEnum = typeof GraphPathResponseStatusEnum[keyof typeof GraphPathResponseStatusEnum];

/**
 *
 * @export
 * @interface GraphProvenance
 */
export interface GraphProvenance {
    /**
     *
     * @type {string}
     * @memberof GraphProvenance
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphProvenance
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphProvenance
     */
    source_span_id: string;
}
/**
 *
 * @export
 * @interface GraphRelationDetailResponse
 */
export interface GraphRelationDetailResponse {
    /**
     *
     * @type {GraphEdge}
     * @memberof GraphRelationDetailResponse
     */
    edge: GraphEdge;
    /**
     *
     * @type {GraphConfirmation}
     * @memberof GraphRelationDetailResponse
     */
    confirmation: GraphConfirmation | null;
    /**
     *
     * @type {string}
     * @memberof GraphRelationDetailResponse
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationDetailResponse
     */
    valid_from: string | null;
    /**
     *
     * @type {string}
     * @memberof GraphRelationDetailResponse
     */
    valid_to: string | null;
    /**
     *
     * @type {string}
     * @memberof GraphRelationDetailResponse
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationDetailResponse
     */
    updated_at: string;
}
/**
 *
 * @export
 * @interface GraphRelationEvidenceItem
 */
export interface GraphRelationEvidenceItem {
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    relation_id: string;
    /**
     *
     * @type {GraphProvenance}
     * @memberof GraphRelationEvidenceItem
     */
    provenance: GraphProvenance;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    reason: string;
    /**
     *
     * @type {GraphApplicability}
     * @memberof GraphRelationEvidenceItem
     */
    applicability: GraphApplicability;
    /**
     *
     * @type {GraphConfirmation}
     * @memberof GraphRelationEvidenceItem
     */
    confirmation: GraphConfirmation | null;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    source_href: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    span_href: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceItem
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface GraphRelationEvidenceResponse
 */
export interface GraphRelationEvidenceResponse {
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceResponse
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphRelationEvidenceResponse
     */
    relation_id: string;
    /**
     *
     * @type {Array<GraphRelationEvidenceItem>}
     * @memberof GraphRelationEvidenceResponse
     */
    items: Array<GraphRelationEvidenceItem>;
    /**
     *
     * @type {GraphPageMeta}
     * @memberof GraphRelationEvidenceResponse
     */
    meta: GraphPageMeta;
}
/**
 *
 * @export
 * @interface GraphTopicNode
 */
export interface GraphTopicNode {
    /**
     *
     * @type {GraphTopicNodeTypeEnum}
     * @memberof GraphTopicNode
     */
    type: GraphTopicNodeTypeEnum;
    /**
     *
     * @type {string}
     * @memberof GraphTopicNode
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof GraphTopicNode
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof GraphTopicNode
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof GraphTopicNode
     */
    description: string;
    /**
     *
     * @type {GraphTopicNodeTopicStatusEnum}
     * @memberof GraphTopicNode
     */
    topic_status: GraphTopicNodeTopicStatusEnum;
    /**
     *
     * @type {number}
     * @memberof GraphTopicNode
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof GraphTopicNode
     */
    updated_at: string;
}


/**
 * @export
 */
export const GraphTopicNodeTypeEnum = {
    Topic: 'TOPIC'
} as const;
export type GraphTopicNodeTypeEnum = typeof GraphTopicNodeTypeEnum[keyof typeof GraphTopicNodeTypeEnum];

/**
 * @export
 */
export const GraphTopicNodeTopicStatusEnum = {
    Active: 'ACTIVE',
    Merged: 'MERGED',
    Deprecated: 'DEPRECATED'
} as const;
export type GraphTopicNodeTopicStatusEnum = typeof GraphTopicNodeTopicStatusEnum[keyof typeof GraphTopicNodeTopicStatusEnum];

/**
 *
 * @export
 * @interface HealthCapabilityAvailability
 */
export interface HealthCapabilityAvailability {
    /**
     *
     * @type {string}
     * @memberof HealthCapabilityAvailability
     */
    code: string;
    /**
     *
     * @type {string}
     * @memberof HealthCapabilityAvailability
     */
    reason: string;
}
/**
 *
 * @export
 * @interface HealthDecisionRequest
 */
export interface HealthDecisionRequest {
    /**
     *
     * @type {string}
     * @memberof HealthDecisionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof HealthDecisionRequest
     */
    expected_version: number;
    /**
     *
     * @type {HealthDecisionRequestActionEnum}
     * @memberof HealthDecisionRequest
     */
    action: HealthDecisionRequestActionEnum;
    /**
     *
     * @type {string}
     * @memberof HealthDecisionRequest
     */
    reason?: string;
    /**
     *
     * @type {string}
     * @memberof HealthDecisionRequest
     */
    deferred_until?: string;
    /**
     *
     * @type {string}
     * @memberof HealthDecisionRequest
     */
    proposal_id?: string;
    /**
     *
     * @type {string}
     * @memberof HealthDecisionRequest
     */
    repair_option_code?: string;
}


/**
 * @export
 */
export const HealthDecisionRequestActionEnum = {
    Acknowledge: 'ACKNOWLEDGE',
    Ignore: 'IGNORE',
    FalsePositive: 'FALSE_POSITIVE',
    Defer: 'DEFER',
    CreateRepairProposal: 'CREATE_REPAIR_PROPOSAL'
} as const;
export type HealthDecisionRequestActionEnum = typeof HealthDecisionRequestActionEnum[keyof typeof HealthDecisionRequestActionEnum];

/**
 *
 * @export
 * @interface HealthDetectorCoverage
 */
export interface HealthDetectorCoverage {
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverage
     */
    detector_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverage
     */
    detector_version: string;
    /**
     *
     * @type {HealthDetectorCoverageStatus}
     * @memberof HealthDetectorCoverage
     */
    status: HealthDetectorCoverageStatus;
    /**
     *
     * @type {HealthScanCheckpoint}
     * @memberof HealthDetectorCoverage
     */
    checkpoint: HealthScanCheckpoint;
    /**
     *
     * @type {HealthScanCounters}
     * @memberof HealthDetectorCoverage
     */
    counters: HealthScanCounters;
    /**
     *
     * @type {HealthFailureSummary}
     * @memberof HealthDetectorCoverage
     */
    last_error?: HealthFailureSummary;
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverage
     */
    unavailable_reason?: string;
}


/**
 *
 * @export
 * @interface HealthDetectorCoverageStart
 */
export interface HealthDetectorCoverageStart {
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverageStart
     */
    detector_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverageStart
     */
    detector_version: string;
    /**
     *
     * @type {HealthDetectorCoverageStartStatusEnum}
     * @memberof HealthDetectorCoverageStart
     */
    status?: HealthDetectorCoverageStartStatusEnum;
    /**
     *
     * @type {string}
     * @memberof HealthDetectorCoverageStart
     */
    unavailable_reason?: string;
}


/**
 * @export
 */
export const HealthDetectorCoverageStartStatusEnum = {
    Pending: 'PENDING',
    Unavailable: 'UNAVAILABLE'
} as const;
export type HealthDetectorCoverageStartStatusEnum = typeof HealthDetectorCoverageStartStatusEnum[keyof typeof HealthDetectorCoverageStartStatusEnum];


/**
 *
 * @export
 */
export const HealthDetectorCoverageStatus = {
    Pending: 'PENDING',
    Running: 'RUNNING',
    Succeeded: 'SUCCEEDED',
    Partial: 'PARTIAL',
    Failed: 'FAILED',
    Cancelled: 'CANCELLED',
    Unavailable: 'UNAVAILABLE'
} as const;
export type HealthDetectorCoverageStatus = typeof HealthDetectorCoverageStatus[keyof typeof HealthDetectorCoverageStatus];

/**
 *
 * @export
 * @interface HealthFailureSummary
 */
export interface HealthFailureSummary {
    /**
     *
     * @type {string}
     * @memberof HealthFailureSummary
     */
    stage: string;
    /**
     *
     * @type {string}
     * @memberof HealthFailureSummary
     */
    code: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthFailureSummary
     */
    retryable: boolean;
}
/**
 *
 * @export
 * @interface HealthIssue
 */
export interface HealthIssue {
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    workspace_id: string;
    /**
     *
     * @type {HealthIssueType}
     * @memberof HealthIssue
     */
    type: HealthIssueType;
    /**
     *
     * @type {HealthObjectRef}
     * @memberof HealthIssue
     */
    target: HealthObjectRef;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    detector_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    detector_version: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    identity_hash: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    fingerprint: string;
    /**
     *
     * @type {HealthSeverity}
     * @memberof HealthIssue
     */
    severity: HealthSeverity;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    evidence_summary: string;
    /**
     *
     * @type {Array<HealthIssueEvidence>}
     * @memberof HealthIssue
     */
    evidence?: Array<HealthIssueEvidence>;
    /**
     *
     * @type {Array<HealthObjectVersion>}
     * @memberof HealthIssue
     */
    object_versions?: Array<HealthObjectVersion>;
    /**
     *
     * @type {Array<HealthRepairOption>}
     * @memberof HealthIssue
     */
    repair_options?: Array<HealthRepairOption>;
    /**
     *
     * @type {HealthIssueStatus}
     * @memberof HealthIssue
     */
    status: HealthIssueStatus;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    status_reason?: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    deferred_until?: string;
    /**
     *
     * @type {HealthProposalBinding}
     * @memberof HealthIssue
     */
    proposal?: HealthProposalBinding;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    first_detected_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    last_detected_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    last_verified_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    resolved_at?: string;
    /**
     *
     * @type {number}
     * @memberof HealthIssue
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssue
     */
    updated_at: string;
}


/**
 *
 * @export
 * @interface HealthIssueDecision
 */
export interface HealthIssueDecision {
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof HealthIssueDecision
     */
    issue_version: number;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    proposal_id?: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    idempotency_key: string;
    /**
     *
     * @type {HealthIssueDecisionActionEnum}
     * @memberof HealthIssueDecision
     */
    action: HealthIssueDecisionActionEnum;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    reason?: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    deferred_until?: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecision
     */
    created_at: string;
}


/**
 * @export
 */
export const HealthIssueDecisionActionEnum = {
    Acknowledge: 'ACKNOWLEDGE',
    Ignore: 'IGNORE',
    FalsePositive: 'FALSE_POSITIVE',
    Defer: 'DEFER',
    CreateRepairProposal: 'CREATE_REPAIR_PROPOSAL'
} as const;
export type HealthIssueDecisionActionEnum = typeof HealthIssueDecisionActionEnum[keyof typeof HealthIssueDecisionActionEnum];

/**
 *
 * @export
 * @interface HealthIssueDecisionPage
 */
export interface HealthIssueDecisionPage {
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecisionPage
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecisionPage
     */
    issue_id: string;
    /**
     *
     * @type {Array<HealthIssueDecision>}
     * @memberof HealthIssueDecisionPage
     */
    items: Array<HealthIssueDecision>;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDecisionPage
     */
    next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthIssueDecisionPage
     */
    has_more: boolean;
}
/**
 *
 * @export
 * @interface HealthIssueDetail
 */
export interface HealthIssueDetail {
    /**
     *
     * @type {HealthIssue}
     * @memberof HealthIssueDetail
     */
    issue: HealthIssue;
    /**
     *
     * @type {HealthIssueObservation}
     * @memberof HealthIssueDetail
     */
    latest_observation: HealthIssueObservation;
    /**
     *
     * @type {Array<HealthIssueObservation>}
     * @memberof HealthIssueDetail
     */
    observations: Array<HealthIssueObservation>;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDetail
     */
    observations_next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthIssueDetail
     */
    observations_has_more: boolean;
    /**
     *
     * @type {Array<HealthIssueDecision>}
     * @memberof HealthIssueDetail
     */
    decisions: Array<HealthIssueDecision>;
    /**
     *
     * @type {string}
     * @memberof HealthIssueDetail
     */
    decisions_next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthIssueDetail
     */
    decisions_has_more: boolean;
}
/**
 *
 * @export
 * @interface HealthIssueEvidence
 */
export interface HealthIssueEvidence {
    /**
     *
     * @type {HealthObjectRef}
     * @memberof HealthIssueEvidence
     */
    ref: HealthObjectRef;
    /**
     *
     * @type {string}
     * @memberof HealthIssueEvidence
     */
    hash: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueEvidence
     */
    summary: string;
}
/**
 *
 * @export
 * @interface HealthIssueListItem
 */
export interface HealthIssueListItem {
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    workspace_id: string;
    /**
     *
     * @type {HealthIssueType}
     * @memberof HealthIssueListItem
     */
    type: HealthIssueType;
    /**
     *
     * @type {HealthObjectRef}
     * @memberof HealthIssueListItem
     */
    target: HealthObjectRef;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    detector_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    detector_version: string;
    /**
     *
     * @type {HealthSeverity}
     * @memberof HealthIssueListItem
     */
    severity: HealthSeverity;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    evidence_summary: string;
    /**
     *
     * @type {HealthIssueStatus}
     * @memberof HealthIssueListItem
     */
    status: HealthIssueStatus;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    first_detected_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    last_detected_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    last_verified_at: string;
    /**
     *
     * @type {number}
     * @memberof HealthIssueListItem
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthIssueListItem
     */
    updated_at: string;
}


/**
 *
 * @export
 * @interface HealthIssueObservation
 */
export interface HealthIssueObservation {
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof HealthIssueObservation
     */
    issue_version: number;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    scan_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    detector_version: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    evidence_fingerprint: string;
    /**
     *
     * @type {Array<HealthObjectVersion>}
     * @memberof HealthIssueObservation
     */
    target_versions: Array<HealthObjectVersion>;
    /**
     *
     * @type {HealthSeverity}
     * @memberof HealthIssueObservation
     */
    severity: HealthSeverity;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservation
     */
    observed_at: string;
    /**
     *
     * @type {Array<HealthIssueEvidence>}
     * @memberof HealthIssueObservation
     */
    evidence: Array<HealthIssueEvidence>;
}


/**
 *
 * @export
 * @interface HealthIssueObservationPage
 */
export interface HealthIssueObservationPage {
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservationPage
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservationPage
     */
    issue_id: string;
    /**
     *
     * @type {Array<HealthIssueObservation>}
     * @memberof HealthIssueObservationPage
     */
    items: Array<HealthIssueObservation>;
    /**
     *
     * @type {string}
     * @memberof HealthIssueObservationPage
     */
    next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthIssueObservationPage
     */
    has_more: boolean;
}
/**
 *
 * @export
 * @interface HealthIssuePage
 */
export interface HealthIssuePage {
    /**
     *
     * @type {string}
     * @memberof HealthIssuePage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<HealthIssueListItem>}
     * @memberof HealthIssuePage
     */
    items: Array<HealthIssueListItem>;
    /**
     *
     * @type {string}
     * @memberof HealthIssuePage
     */
    next_cursor?: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthIssuePage
     */
    has_more: boolean;
}

/**
 *
 * @export
 */
export const HealthIssueStatus = {
    Open: 'OPEN',
    Acknowledged: 'ACKNOWLEDGED',
    Deferred: 'DEFERRED',
    ProposalCreated: 'PROPOSAL_CREATED',
    Resolved: 'RESOLVED',
    Ignored: 'IGNORED',
    FalsePositive: 'FALSE_POSITIVE',
    Reopened: 'REOPENED'
} as const;
export type HealthIssueStatus = typeof HealthIssueStatus[keyof typeof HealthIssueStatus];


/**
 *
 * @export
 */
export const HealthIssueType = {
    Orphan: 'ORPHAN',
    Duplicate: 'DUPLICATE',
    Conflict: 'CONFLICT',
    Stale: 'STALE',
    MissingSource: 'MISSING_SOURCE',
    LowConfidence: 'LOW_CONFIDENCE',
    BrokenReference: 'BROKEN_REFERENCE',
    IndexError: 'INDEX_ERROR',
    SupersededUsage: 'SUPERSEDED_USAGE',
    ReviewInvalidated: 'REVIEW_INVALIDATED'
} as const;
export type HealthIssueType = typeof HealthIssueType[keyof typeof HealthIssueType];

/**
 *
 * @export
 * @interface HealthObjectRef
 */
export interface HealthObjectRef {
    /**
     *
     * @type {HealthObjectType}
     * @memberof HealthObjectRef
     */
    type: HealthObjectType;
    /**
     *
     * @type {string}
     * @memberof HealthObjectRef
     */
    id: string;
}



/**
 *
 * @export
 */
export const HealthObjectType = {
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    SourceVersion: 'SOURCE_VERSION',
    IndexVersion: 'INDEX_VERSION'
} as const;
export type HealthObjectType = typeof HealthObjectType[keyof typeof HealthObjectType];

/**
 *
 * @export
 * @interface HealthObjectVersion
 */
export interface HealthObjectVersion {
    /**
     *
     * @type {HealthObjectRef}
     * @memberof HealthObjectVersion
     */
    ref: HealthObjectRef;
    /**
     *
     * @type {number}
     * @memberof HealthObjectVersion
     */
    version: number;
}
/**
 *
 * @export
 * @interface HealthProposalBinding
 */
export interface HealthProposalBinding {
    /**
     *
     * @type {string}
     * @memberof HealthProposalBinding
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthProposalBinding
     */
    repair_option_code: string;
    /**
     *
     * @type {string}
     * @memberof HealthProposalBinding
     */
    fingerprint: string;
    /**
     *
     * @type {Array<HealthObjectVersion>}
     * @memberof HealthProposalBinding
     */
    object_versions: Array<HealthObjectVersion>;
    /**
     *
     * @type {string}
     * @memberof HealthProposalBinding
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface HealthRepairOption
 */
export interface HealthRepairOption {
    /**
     *
     * @type {string}
     * @memberof HealthRepairOption
     */
    code: string;
    /**
     *
     * @type {string}
     * @memberof HealthRepairOption
     */
    title: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthRepairOption
     */
    available: boolean;
    /**
     *
     * @type {string}
     * @memberof HealthRepairOption
     */
    unavailable_reason?: string;
}
/**
 *
 * @export
 * @interface HealthScan
 */
export interface HealthScan {
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    workflow_run_id: string;
    /**
     *
     * @type {HealthScanScopeBinding}
     * @memberof HealthScan
     */
    scope: HealthScanScopeBinding;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    request_hash: string;
    /**
     *
     * @type {number}
     * @memberof HealthScan
     */
    max_items: number;
    /**
     *
     * @type {HealthScanStatus}
     * @memberof HealthScan
     */
    status: HealthScanStatus;
    /**
     *
     * @type {HealthScanCheckpoint}
     * @memberof HealthScan
     */
    checkpoint: HealthScanCheckpoint;
    /**
     *
     * @type {HealthScanCounters}
     * @memberof HealthScan
     */
    counters: HealthScanCounters;
    /**
     *
     * @type {Array<HealthDetectorCoverage>}
     * @memberof HealthScan
     */
    coverage: Array<HealthDetectorCoverage>;
    /**
     *
     * @type {HealthFailureSummary}
     * @memberof HealthScan
     */
    last_error: HealthFailureSummary | null;
    /**
     *
     * @type {number}
     * @memberof HealthScan
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    completed_at: string | null;
    /**
     *
     * @type {string}
     * @memberof HealthScan
     */
    status_url: string;
}


/**
 *
 * @export
 * @interface HealthScanAcceptance
 */
export interface HealthScanAcceptance {
    /**
     *
     * @type {HealthScan}
     * @memberof HealthScanAcceptance
     */
    scan: HealthScan;
    /**
     *
     * @type {string}
     * @memberof HealthScanAcceptance
     */
    health_scan_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthScanAcceptance
     */
    workflow_run_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthScanAcceptance
     */
    status_url: string;
    /**
     *
     * @type {boolean}
     * @memberof HealthScanAcceptance
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface HealthScanCheckpoint
 */
export interface HealthScanCheckpoint {
    /**
     *
     * @type {string}
     * @memberof HealthScanCheckpoint
     */
    cursor: string;
    /**
     *
     * @type {number}
     * @memberof HealthScanCheckpoint
     */
    page: number;
    /**
     *
     * @type {HealthObjectRef}
     * @memberof HealthScanCheckpoint
     */
    last_item: HealthObjectRef | null;
}
/**
 *
 * @export
 * @interface HealthScanCounters
 */
export interface HealthScanCounters {
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    processed: number;
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    created: number;
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    reopened: number;
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    resolved: number;
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    unchanged: number;
    /**
     *
     * @type {number}
     * @memberof HealthScanCounters
     */
    failed: number;
}
/**
 * @type HealthScanScopeBinding
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type HealthScanScopeBinding = HealthScanScopeBindingOneOf | HealthScanScopeBindingOneOf1;

/**
 *
 * @export
 * @interface HealthScanScopeBindingOneOf
 */
export interface HealthScanScopeBindingOneOf {
    /**
     *
     * @type {HealthScanScopeBindingOneOfTypeEnum}
     * @memberof HealthScanScopeBindingOneOf
     */
    type: HealthScanScopeBindingOneOfTypeEnum;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf
     */
    ref: string;
    /**
     *
     * @type {number}
     * @memberof HealthScanScopeBindingOneOf
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf
     */
    schema_version: string;
}


/**
 * @export
 */
export const HealthScanScopeBindingOneOfTypeEnum = {
    Workspace: 'WORKSPACE',
    Topic: 'TOPIC',
    Directory: 'DIRECTORY'
} as const;
export type HealthScanScopeBindingOneOfTypeEnum = typeof HealthScanScopeBindingOneOfTypeEnum[keyof typeof HealthScanScopeBindingOneOfTypeEnum];

/**
 *
 * @export
 * @interface HealthScanScopeBindingOneOf1
 */
export interface HealthScanScopeBindingOneOf1 {
    /**
     *
     * @type {HealthScanScopeBindingOneOf1TypeEnum}
     * @memberof HealthScanScopeBindingOneOf1
     */
    type: HealthScanScopeBindingOneOf1TypeEnum;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf1
     */
    ref: string;
    /**
     *
     * @type {number}
     * @memberof HealthScanScopeBindingOneOf1
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf1
     */
    schema_version: string;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf1
     */
    hash: string;
    /**
     *
     * @type {string}
     * @memberof HealthScanScopeBindingOneOf1
     */
    read_model_revision: string;
    /**
     *
     * @type {number}
     * @memberof HealthScanScopeBindingOneOf1
     */
    exact_count: number;
}


/**
 * @export
 */
export const HealthScanScopeBindingOneOf1TypeEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type HealthScanScopeBindingOneOf1TypeEnum = typeof HealthScanScopeBindingOneOf1TypeEnum[keyof typeof HealthScanScopeBindingOneOf1TypeEnum];


/**
 *
 * @export
 */
export const HealthScanScopeType = {
    Workspace: 'WORKSPACE',
    Topic: 'TOPIC',
    SmartCollection: 'SMART_COLLECTION',
    Directory: 'DIRECTORY'
} as const;
export type HealthScanScopeType = typeof HealthScanScopeType[keyof typeof HealthScanScopeType];

/**
 *
 * @export
 * @interface HealthScanStartRequest
 */
export interface HealthScanStartRequest {
    /**
     *
     * @type {string}
     * @memberof HealthScanStartRequest
     */
    workspace_id: string;
    /**
     *
     * @type {HealthScanScopeBinding}
     * @memberof HealthScanStartRequest
     */
    scope: HealthScanScopeBinding;
    /**
     *
     * @type {Array<HealthDetectorCoverageStart>}
     * @memberof HealthScanStartRequest
     */
    coverage?: Array<HealthDetectorCoverageStart>;
    /**
     *
     * @type {number}
     * @memberof HealthScanStartRequest
     */
    max_items: number;
    /**
     *
     * @type {boolean}
     * @memberof HealthScanStartRequest
     */
    prevent_scope_concurrency?: boolean;
}

/**
 *
 * @export
 */
export const HealthScanStatus = {
    Pending: 'PENDING',
    Running: 'RUNNING',
    Succeeded: 'SUCCEEDED',
    Partial: 'PARTIAL',
    Failed: 'FAILED',
    Cancelled: 'CANCELLED'
} as const;
export type HealthScanStatus = typeof HealthScanStatus[keyof typeof HealthScanStatus];

/**
 *
 * @export
 * @interface HealthScanSummary
 */
export interface HealthScanSummary {
    /**
     *
     * @type {string}
     * @memberof HealthScanSummary
     */
    id: string;
    /**
     *
     * @type {HealthScanStatus}
     * @memberof HealthScanSummary
     */
    status: HealthScanStatus;
    /**
     *
     * @type {HealthScanScopeBinding}
     * @memberof HealthScanSummary
     */
    scope: HealthScanScopeBinding;
    /**
     *
     * @type {string}
     * @memberof HealthScanSummary
     */
    completed_at?: string;
    /**
     *
     * @type {HealthScanCounters}
     * @memberof HealthScanSummary
     */
    counters: HealthScanCounters;
    /**
     *
     * @type {Array<HealthDetectorCoverage>}
     * @memberof HealthScanSummary
     */
    coverage: Array<HealthDetectorCoverage>;
    /**
     *
     * @type {string}
     * @memberof HealthScanSummary
     */
    updated_at: string;
}


/**
 *
 * @export
 * @interface HealthSchedule
 */
export interface HealthSchedule {
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    workspace_id: string;
    /**
     *
     * @type {HealthScheduleScope}
     * @memberof HealthSchedule
     */
    scope: HealthScheduleScope;
    /**
     *
     * @type {HealthScheduleCadence}
     * @memberof HealthSchedule
     */
    cadence: HealthScheduleCadence;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    cron_expression: string;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    timezone: string;
    /**
     *
     * @type {number}
     * @memberof HealthSchedule
     */
    max_items: number;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    next_run_at: string | null;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    last_run_at: string | null;
    /**
     *
     * @type {number}
     * @memberof HealthSchedule
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof HealthSchedule
     */
    updated_at: string;
}



/**
 *
 * @export
 */
export const HealthScheduleCadence = {
    Disabled: 'DISABLED',
    Daily: 'DAILY',
    Weekly: 'WEEKLY',
    Cron: 'CRON'
} as const;
export type HealthScheduleCadence = typeof HealthScheduleCadence[keyof typeof HealthScheduleCadence];

/**
 * @type HealthScheduleScope
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type HealthScheduleScope = HealthScanScopeBindingOneOf | HealthScheduleScopeOneOf;

/**
 *
 * @export
 * @interface HealthScheduleScopeOneOf
 */
export interface HealthScheduleScopeOneOf {
    /**
     *
     * @type {HealthScheduleScopeOneOfTypeEnum}
     * @memberof HealthScheduleScopeOneOf
     */
    type: HealthScheduleScopeOneOfTypeEnum;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleScopeOneOf
     */
    ref: string;
    /**
     *
     * @type {number}
     * @memberof HealthScheduleScopeOneOf
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleScopeOneOf
     */
    schema_version: string;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleScopeOneOf
     */
    hash: string;
}


/**
 * @export
 */
export const HealthScheduleScopeOneOfTypeEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type HealthScheduleScopeOneOfTypeEnum = typeof HealthScheduleScopeOneOfTypeEnum[keyof typeof HealthScheduleScopeOneOfTypeEnum];

/**
 *
 * @export
 * @interface HealthScheduleUpdateRequest
 */
export interface HealthScheduleUpdateRequest {
    /**
     *
     * @type {string}
     * @memberof HealthScheduleUpdateRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleUpdateRequest
     */
    schedule_id?: string;
    /**
     *
     * @type {HealthScheduleScope}
     * @memberof HealthScheduleUpdateRequest
     */
    scope?: HealthScheduleScope;
    /**
     *
     * @type {number}
     * @memberof HealthScheduleUpdateRequest
     */
    expected_version?: number;
    /**
     *
     * @type {HealthScheduleCadence}
     * @memberof HealthScheduleUpdateRequest
     */
    cadence: HealthScheduleCadence;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleUpdateRequest
     */
    cron_expression?: string;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleUpdateRequest
     */
    timezone: string;
    /**
     *
     * @type {number}
     * @memberof HealthScheduleUpdateRequest
     */
    max_items: number;
    /**
     *
     * @type {string}
     * @memberof HealthScheduleUpdateRequest
     */
    next_run_at?: string | null;
}



/**
 *
 * @export
 */
export const HealthSeverity = {
    Critical: 'CRITICAL',
    High: 'HIGH',
    Medium: 'MEDIUM',
    Low: 'LOW'
} as const;
export type HealthSeverity = typeof HealthSeverity[keyof typeof HealthSeverity];

/**
 *
 * @export
 * @interface HealthSummary
 */
export interface HealthSummary {
    /**
     *
     * @type {string}
     * @memberof HealthSummary
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof HealthSummary
     */
    open_count: number;
    /**
     *
     * @type {{ [key: string]: number; }}
     * @memberof HealthSummary
     */
    open_by_severity: { [key: string]: number; };
    /**
     *
     * @type {{ [key: string]: number; }}
     * @memberof HealthSummary
     */
    open_by_type: { [key: string]: number; };
    /**
     *
     * @type {Array<HealthTrendPoint>}
     * @memberof HealthSummary
     */
    trend: Array<HealthTrendPoint>;
    /**
     *
     * @type {HealthScanSummary}
     * @memberof HealthSummary
     */
    last_scan?: HealthScanSummary;
    /**
     *
     * @type {Array<HealthCapabilityAvailability>}
     * @memberof HealthSummary
     */
    unavailable?: Array<HealthCapabilityAvailability>;
}
/**
 *
 * @export
 * @interface HealthTrendPoint
 */
export interface HealthTrendPoint {
    /**
     *
     * @type {string}
     * @memberof HealthTrendPoint
     */
    date: string;
    /**
     *
     * @type {number}
     * @memberof HealthTrendPoint
     */
    detected_count: number;
    /**
     *
     * @type {number}
     * @memberof HealthTrendPoint
     */
    resolved_count: number;
}
/**
 *
 * @export
 * @interface HistoricalProposalRevision
 */
export interface HistoricalProposalRevision {
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof HistoricalProposalRevision
     */
    revision_no: number;
    /**
     *
     * @type {HistoricalProposalRevisionTargetModeEnum}
     * @memberof HistoricalProposalRevision
     */
    target_mode: HistoricalProposalRevisionTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    base_hash: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    evidence_summary: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof HistoricalProposalRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const HistoricalProposalRevisionTargetModeEnum = {
    Replace: 'REPLACE'
} as const;
export type HistoricalProposalRevisionTargetModeEnum = typeof HistoricalProposalRevisionTargetModeEnum[keyof typeof HistoricalProposalRevisionTargetModeEnum];

/**
 *
 * @export
 * @interface HumanDecisionRequest
 */
export interface HumanDecisionRequest {
    /**
     *
     * @type {number}
     * @memberof HumanDecisionRequest
     */
    target_version: number;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof HumanDecisionRequest
     */
    decision: { [key: string]: JSONValue; };
}
/**
 *
 * @export
 * @interface HumanTask
 */
export interface HumanTask {
    /**
     *
     * @type {string}
     * @memberof HumanTask
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof HumanTask
     */
    run_id: string;
    /**
     *
     * @type {string}
     * @memberof HumanTask
     */
    node_run_id: string;
    /**
     *
     * @type {HumanTaskStatusEnum}
     * @memberof HumanTask
     */
    status: HumanTaskStatusEnum;
    /**
     *
     * @type {number}
     * @memberof HumanTask
     */
    target_version: number;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof HumanTask
     */
    decision?: { [key: string]: JSONValue; };
    /**
     *
     * @type {string}
     * @memberof HumanTask
     */
    submitted_at?: string;
}


/**
 * @export
 */
export const HumanTaskStatusEnum = {
    Pending: 'pending',
    Submitted: 'submitted',
    Expired: 'expired',
    Cancelled: 'cancelled'
} as const;
export type HumanTaskStatusEnum = typeof HumanTaskStatusEnum[keyof typeof HumanTaskStatusEnum];


/**
 *
 * @export
 */
export const ImpactAction = {
    Review: 'REVIEW',
    Reindex: 'REINDEX',
    ResolveConflict: 'RESOLVE_CONFLICT',
    RefreshHealth: 'REFRESH_HEALTH',
    RegenerateArtifact: 'REGENERATE_ARTIFACT',
    RevalidateReviewCard: 'REVALIDATE_REVIEW_CARD',
    NoAction: 'NO_ACTION'
} as const;
export type ImpactAction = typeof ImpactAction[keyof typeof ImpactAction];

/**
 *
 * @export
 * @interface ImpactAnalysisResult
 */
export interface ImpactAnalysisResult {
    /**
     *
     * @type {ImpactReport}
     * @memberof ImpactAnalysisResult
     */
    report: ImpactReport;
    /**
     *
     * @type {Array<ImpactProposalDraft>}
     * @memberof ImpactAnalysisResult
     */
    proposal_drafts: Array<ImpactProposalDraft>;
    /**
     *
     * @type {boolean}
     * @memberof ImpactAnalysisResult
     */
    replayed: boolean;
}
/**
 * @type ImpactObject
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ImpactObject = { type: 'ARTICLE_REVISION' } & ImpactObjectV2Legacy | { type: 'ARTIFACT' } & ArtifactImpactObject | { type: 'AUDIT_EVENT' } & ImpactObjectV2Legacy | { type: 'CLAIM' } & ImpactObjectV2Legacy | { type: 'CONFLICT' } & ImpactObjectV2Legacy | { type: 'HEALTH_ISSUE' } & ImpactObjectV2Legacy | { type: 'PROPOSAL' } & ImpactObjectV2Legacy | { type: 'RELATION' } & ImpactObjectV2Legacy | { type: 'REVIEW_CARD' } & ReviewCardImpactObject | { type: 'TOPIC' } & ImpactObjectV2Legacy;


/**
 *
 * @export
 */
export const ImpactObjectType = {
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    HealthIssue: 'HEALTH_ISSUE',
    Proposal: 'PROPOSAL',
    ArticleRevision: 'ARTICLE_REVISION',
    AuditEvent: 'AUDIT_EVENT',
    Artifact: 'ARTIFACT',
    ReviewCard: 'REVIEW_CARD'
} as const;
export type ImpactObjectType = typeof ImpactObjectType[keyof typeof ImpactObjectType];

/**
 *
 * @export
 * @interface ImpactObjectV1
 */
export interface ImpactObjectV1 {
    /**
     *
     * @type {ImpactObjectV1TypeEnum}
     * @memberof ImpactObjectV1
     */
    type: ImpactObjectV1TypeEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV1
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV1
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ImpactObjectV1
     */
    version: number;
    /**
     *
     * @type {ImpactObjectV1ActionEnum}
     * @memberof ImpactObjectV1
     */
    action: ImpactObjectV1ActionEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV1
     */
    reason: string;
    /**
     *
     * @type {boolean}
     * @memberof ImpactObjectV1
     */
    requires_proposal: boolean;
}


/**
 * @export
 */
export const ImpactObjectV1TypeEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    HealthIssue: 'HEALTH_ISSUE',
    Proposal: 'PROPOSAL',
    ArticleRevision: 'ARTICLE_REVISION',
    AuditEvent: 'AUDIT_EVENT'
} as const;
export type ImpactObjectV1TypeEnum = typeof ImpactObjectV1TypeEnum[keyof typeof ImpactObjectV1TypeEnum];

/**
 * @export
 */
export const ImpactObjectV1ActionEnum = {
    Review: 'REVIEW',
    Reindex: 'REINDEX',
    ResolveConflict: 'RESOLVE_CONFLICT',
    RefreshHealth: 'REFRESH_HEALTH',
    NoAction: 'NO_ACTION'
} as const;
export type ImpactObjectV1ActionEnum = typeof ImpactObjectV1ActionEnum[keyof typeof ImpactObjectV1ActionEnum];

/**
 *
 * @export
 * @interface ImpactObjectV2Base
 */
export interface ImpactObjectV2Base {
    /**
     *
     * @type {ImpactObjectType}
     * @memberof ImpactObjectV2Base
     */
    type: ImpactObjectType;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Base
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Base
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ImpactObjectV2Base
     */
    version: number;
    /**
     *
     * @type {ImpactAction}
     * @memberof ImpactObjectV2Base
     */
    action: ImpactAction;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Base
     */
    reason: string;
    /**
     *
     * @type {boolean}
     * @memberof ImpactObjectV2Base
     */
    requires_proposal: boolean;
}


/**
 *
 * @export
 * @interface ImpactObjectV2Legacy
 */
export interface ImpactObjectV2Legacy {
    /**
     *
     * @type {ImpactObjectV2LegacyTypeEnum}
     * @memberof ImpactObjectV2Legacy
     */
    type: ImpactObjectV2LegacyTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Legacy
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Legacy
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ImpactObjectV2Legacy
     */
    version: number;
    /**
     *
     * @type {ImpactAction}
     * @memberof ImpactObjectV2Legacy
     */
    action: ImpactAction;
    /**
     *
     * @type {string}
     * @memberof ImpactObjectV2Legacy
     */
    reason: string;
    /**
     *
     * @type {boolean}
     * @memberof ImpactObjectV2Legacy
     */
    requires_proposal: boolean;
}


/**
 * @export
 */
export const ImpactObjectV2LegacyTypeEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    HealthIssue: 'HEALTH_ISSUE',
    Proposal: 'PROPOSAL',
    ArticleRevision: 'ARTICLE_REVISION',
    AuditEvent: 'AUDIT_EVENT'
} as const;
export type ImpactObjectV2LegacyTypeEnum = typeof ImpactObjectV2LegacyTypeEnum[keyof typeof ImpactObjectV2LegacyTypeEnum];

/**
 *
 * @export
 * @interface ImpactProposalDraft
 */
export interface ImpactProposalDraft {
    /**
     *
     * @type {string}
     * @memberof ImpactProposalDraft
     */
    id?: string;
    /**
     *
     * @type {string}
     * @memberof ImpactProposalDraft
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactProposalDraft
     */
    source_event_id: string;
    /**
     *
     * @type {ImpactProposalDraftOperationEnum}
     * @memberof ImpactProposalDraft
     */
    operation: ImpactProposalDraftOperationEnum;
    /**
     *
     * @type {ImpactProposalDraftTargetTypeEnum}
     * @memberof ImpactProposalDraft
     */
    target_type: ImpactProposalDraftTargetTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactProposalDraft
     */
    target_id: string;
    /**
     *
     * @type {number}
     * @memberof ImpactProposalDraft
     */
    base_version: number;
    /**
     *
     * @type {string}
     * @memberof ImpactProposalDraft
     */
    reason: string;
    /**
     *
     * @type {ImpactProposalDraftRequiresApprovalEnum}
     * @memberof ImpactProposalDraft
     */
    requires_approval: ImpactProposalDraftRequiresApprovalEnum;
    /**
     *
     * @type {ImpactProposalDraftRequiresWriteAuthorizationEnum}
     * @memberof ImpactProposalDraft
     */
    requires_write_authorization: ImpactProposalDraftRequiresWriteAuthorizationEnum;
}


/**
 * @export
 */
export const ImpactProposalDraftOperationEnum = {
    Review: 'REVIEW',
    Reindex: 'REINDEX',
    ResolveConflict: 'RESOLVE_CONFLICT',
    RefreshHealth: 'REFRESH_HEALTH',
    RegenerateArtifact: 'REGENERATE_ARTIFACT',
    RevalidateReviewCard: 'REVALIDATE_REVIEW_CARD'
} as const;
export type ImpactProposalDraftOperationEnum = typeof ImpactProposalDraftOperationEnum[keyof typeof ImpactProposalDraftOperationEnum];

/**
 * @export
 */
export const ImpactProposalDraftTargetTypeEnum = {
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    HealthIssue: 'HEALTH_ISSUE',
    Proposal: 'PROPOSAL',
    ArticleRevision: 'ARTICLE_REVISION',
    AuditEvent: 'AUDIT_EVENT',
    Artifact: 'ARTIFACT',
    ReviewCard: 'REVIEW_CARD'
} as const;
export type ImpactProposalDraftTargetTypeEnum = typeof ImpactProposalDraftTargetTypeEnum[keyof typeof ImpactProposalDraftTargetTypeEnum];

/**
 * @export
 */
export const ImpactProposalDraftRequiresApprovalEnum = {
    True: true
} as const;
export type ImpactProposalDraftRequiresApprovalEnum = typeof ImpactProposalDraftRequiresApprovalEnum[keyof typeof ImpactProposalDraftRequiresApprovalEnum];

/**
 * @export
 */
export const ImpactProposalDraftRequiresWriteAuthorizationEnum = {
    True: true
} as const;
export type ImpactProposalDraftRequiresWriteAuthorizationEnum = typeof ImpactProposalDraftRequiresWriteAuthorizationEnum[keyof typeof ImpactProposalDraftRequiresWriteAuthorizationEnum];

/**
 * @type ImpactReport
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ImpactReport = { schema_version: 'impact-report/v1' } & ImpactReportV1 | { schema_version: 'impact-report/v2' } & ImpactReportV2;

/**
 *
 * @export
 * @interface ImpactReportV1
 */
export interface ImpactReportV1 {
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    source_event_id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    source_event_ref: string;
    /**
     *
     * @type {number}
     * @memberof ImpactReportV1
     */
    source_event_version: number;
    /**
     *
     * @type {ImpactReportV1StatusEnum}
     * @memberof ImpactReportV1
     */
    status: ImpactReportV1StatusEnum;
    /**
     *
     * @type {Array<ImpactObjectV1>}
     * @memberof ImpactReportV1
     */
    objects: Array<ImpactObjectV1>;
    /**
     *
     * @type {{ [key: string]: number; }}
     * @memberof ImpactReportV1
     */
    summary: { [key: string]: number; };
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    error_code?: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    stale_reason?: string;
    /**
     *
     * @type {ImpactReportV1SchemaVersionEnum}
     * @memberof ImpactReportV1
     */
    schema_version: ImpactReportV1SchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    generated_at: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV1
     */
    created_at: string;
    /**
     *
     * @type {number}
     * @memberof ImpactReportV1
     */
    version: number;
}


/**
 * @export
 */
export const ImpactReportV1StatusEnum = {
    Ready: 'READY',
    Stale: 'STALE',
    Failed: 'FAILED'
} as const;
export type ImpactReportV1StatusEnum = typeof ImpactReportV1StatusEnum[keyof typeof ImpactReportV1StatusEnum];

/**
 * @export
 */
export const ImpactReportV1SchemaVersionEnum = {
    ImpactReportV1: 'impact-report/v1'
} as const;
export type ImpactReportV1SchemaVersionEnum = typeof ImpactReportV1SchemaVersionEnum[keyof typeof ImpactReportV1SchemaVersionEnum];

/**
 *
 * @export
 * @interface ImpactReportV2
 */
export interface ImpactReportV2 {
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    source_event_id: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    source_event_ref: string;
    /**
     *
     * @type {number}
     * @memberof ImpactReportV2
     */
    source_event_version: number;
    /**
     *
     * @type {ImpactReportV2AnalysisVersionEnum}
     * @memberof ImpactReportV2
     */
    analysis_version: ImpactReportV2AnalysisVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    supersedes_report_id: string | null;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    superseded_by_report_id: string | null;
    /**
     *
     * @type {ImpactReportV2StatusEnum}
     * @memberof ImpactReportV2
     */
    status: ImpactReportV2StatusEnum;
    /**
     *
     * @type {Array<ImpactObject>}
     * @memberof ImpactReportV2
     */
    objects: Array<ImpactObject>;
    /**
     *
     * @type {{ [key: string]: number; }}
     * @memberof ImpactReportV2
     */
    summary: { [key: string]: number; };
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    error_code?: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    stale_reason?: string;
    /**
     *
     * @type {ImpactReportV2SchemaVersionEnum}
     * @memberof ImpactReportV2
     */
    schema_version: ImpactReportV2SchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    generated_at: string;
    /**
     *
     * @type {string}
     * @memberof ImpactReportV2
     */
    created_at: string;
    /**
     *
     * @type {number}
     * @memberof ImpactReportV2
     */
    version: number;
}


/**
 * @export
 */
export const ImpactReportV2AnalysisVersionEnum = {
    ImpactAnalysisV2: 'impact-analysis/v2'
} as const;
export type ImpactReportV2AnalysisVersionEnum = typeof ImpactReportV2AnalysisVersionEnum[keyof typeof ImpactReportV2AnalysisVersionEnum];

/**
 * @export
 */
export const ImpactReportV2StatusEnum = {
    Ready: 'READY',
    Stale: 'STALE',
    Failed: 'FAILED'
} as const;
export type ImpactReportV2StatusEnum = typeof ImpactReportV2StatusEnum[keyof typeof ImpactReportV2StatusEnum];

/**
 * @export
 */
export const ImpactReportV2SchemaVersionEnum = {
    ImpactReportV2: 'impact-report/v2'
} as const;
export type ImpactReportV2SchemaVersionEnum = typeof ImpactReportV2SchemaVersionEnum[keyof typeof ImpactReportV2SchemaVersionEnum];

/**
 *
 * @export
 * @interface IngestionRequest
 */
export interface IngestionRequest {
    /**
     *
     * @type {string}
     * @memberof IngestionRequest
     */
    workflow_run_id?: string;
    /**
     *
     * @type {number}
     * @memberof IngestionRequest
     */
    attempt_number: number;
}
/**
 *
 * @export
 * @interface IngestionResult
 */
export interface IngestionResult {
    /**
     *
     * @type {string}
     * @memberof IngestionResult
     */
    attempt_id: string;
    /**
     *
     * @type {boolean}
     * @memberof IngestionResult
     */
    attempt_created: boolean;
    /**
     *
     * @type {number}
     * @memberof IngestionResult
     */
    attempt_number: number;
    /**
     *
     * @type {IngestionResultStatusEnum}
     * @memberof IngestionResult
     */
    status: IngestionResultStatusEnum;
    /**
     *
     * @type {IngestionResultSecurityStatusEnum}
     * @memberof IngestionResult
     */
    security_status: IngestionResultSecurityStatusEnum;
    /**
     *
     * @type {string}
     * @memberof IngestionResult
     */
    failure_stage?: string;
    /**
     *
     * @type {string}
     * @memberof IngestionResult
     */
    error_code?: string;
    /**
     *
     * @type {boolean}
     * @memberof IngestionResult
     */
    retryable: boolean;
    /**
     *
     * @type {string}
     * @memberof IngestionResult
     */
    parse_projection_id?: string;
    /**
     *
     * @type {boolean}
     * @memberof IngestionResult
     */
    projection_created: boolean;
    /**
     *
     * @type {number}
     * @memberof IngestionResult
     */
    chunk_count: number;
    /**
     *
     * @type {number}
     * @memberof IngestionResult
     */
    warning_count: number;
}


/**
 * @export
 */
export const IngestionResultStatusEnum = {
    Validating: 'validating',
    Parsing: 'parsing',
    Parsed: 'parsed',
    Chunking: 'chunking',
    Chunked: 'chunked',
    ParseFailed: 'parse_failed',
    Cancelled: 'cancelled'
} as const;
export type IngestionResultStatusEnum = typeof IngestionResultStatusEnum[keyof typeof IngestionResultStatusEnum];

/**
 * @export
 */
export const IngestionResultSecurityStatusEnum = {
    Pending: 'pending',
    Passed: 'passed',
    Quarantined: 'quarantined'
} as const;
export type IngestionResultSecurityStatusEnum = typeof IngestionResultSecurityStatusEnum[keyof typeof IngestionResultSecurityStatusEnum];

/**
 *
 * @export
 * @interface InterviewArtifactBinding
 */
export interface InterviewArtifactBinding {
    /**
     *
     * @type {InterviewArtifactBindingKindEnum}
     * @memberof InterviewArtifactBinding
     */
    kind: InterviewArtifactBindingKindEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewArtifactBinding
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewArtifactBinding
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof InterviewArtifactBinding
     */
    artifact_version: number;
}


/**
 * @export
 */
export const InterviewArtifactBindingKindEnum = {
    InterviewDoc: 'INTERVIEW_DOC',
    LearningPath: 'LEARNING_PATH'
} as const;
export type InterviewArtifactBindingKindEnum = typeof InterviewArtifactBindingKindEnum[keyof typeof InterviewArtifactBindingKindEnum];

/**
 *
 * @export
 * @interface InterviewCompletionResult
 */
export interface InterviewCompletionResult {
    /**
     *
     * @type {InterviewSession}
     * @memberof InterviewCompletionResult
     */
    session: InterviewSession;
    /**
     *
     * @type {InterviewReport}
     * @memberof InterviewCompletionResult
     */
    report: InterviewReport;
    /**
     *
     * @type {LearningPath}
     * @memberof InterviewCompletionResult
     */
    path: LearningPath;
    /**
     *
     * @type {Array<LearningPathStep>}
     * @memberof InterviewCompletionResult
     */
    steps: Array<LearningPathStep>;
    /**
     *
     * @type {boolean}
     * @memberof InterviewCompletionResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface InterviewConfig
 */
export interface InterviewConfig {
    /**
     *
     * @type {InterviewConfigSchemaVersionEnum}
     * @memberof InterviewConfig
     */
    schema_version: InterviewConfigSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewConfig
     */
    role: string;
    /**
     *
     * @type {InterviewScope}
     * @memberof InterviewConfig
     */
    scope: InterviewScope | null;
    /**
     *
     * @type {InterviewConfigDifficultyEnum}
     * @memberof InterviewConfig
     */
    difficulty: InterviewConfigDifficultyEnum;
    /**
     *
     * @type {number}
     * @memberof InterviewConfig
     */
    duration_minutes: number;
    /**
     *
     * @type {number}
     * @memberof InterviewConfig
     */
    question_count: number;
    /**
     *
     * @type {number}
     * @memberof InterviewConfig
     */
    max_follow_ups: number;
}


/**
 * @export
 */
export const InterviewConfigSchemaVersionEnum = {
    InterviewV1: 'interview/v1'
} as const;
export type InterviewConfigSchemaVersionEnum = typeof InterviewConfigSchemaVersionEnum[keyof typeof InterviewConfigSchemaVersionEnum];

/**
 * @export
 */
export const InterviewConfigDifficultyEnum = {
    Foundation: 'FOUNDATION',
    Intermediate: 'INTERMEDIATE',
    Advanced: 'ADVANCED'
} as const;
export type InterviewConfigDifficultyEnum = typeof InterviewConfigDifficultyEnum[keyof typeof InterviewConfigDifficultyEnum];

/**
 *
 * @export
 * @interface InterviewEvidence
 */
export interface InterviewEvidence {
    /**
     *
     * @type {InterviewEvidenceSchemaVersionEnum}
     * @memberof InterviewEvidence
     */
    schema_version: InterviewEvidenceSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    evidence_hash: string;
    /**
     *
     * @type {InterviewEvidenceSupportTypeEnum}
     * @memberof InterviewEvidence
     */
    support_type: InterviewEvidenceSupportTypeEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    source_version_href: string;
    /**
     *
     * @type {string}
     * @memberof InterviewEvidence
     */
    source_span_href: string;
}


/**
 * @export
 */
export const InterviewEvidenceSchemaVersionEnum = {
    InterviewEvidenceV1: 'interview-evidence/v1'
} as const;
export type InterviewEvidenceSchemaVersionEnum = typeof InterviewEvidenceSchemaVersionEnum[keyof typeof InterviewEvidenceSchemaVersionEnum];

/**
 * @export
 */
export const InterviewEvidenceSupportTypeEnum = {
    Supports: 'SUPPORTS'
} as const;
export type InterviewEvidenceSupportTypeEnum = typeof InterviewEvidenceSupportTypeEnum[keyof typeof InterviewEvidenceSupportTypeEnum];

/**
 *
 * @export
 * @interface InterviewFinding
 */
export interface InterviewFinding {
    /**
     *
     * @type {string}
     * @memberof InterviewFinding
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewFinding
     */
    topic_id?: string;
    /**
     *
     * @type {string}
     * @memberof InterviewFinding
     */
    detail: string;
    /**
     *
     * @type {Array<InterviewEvidence>}
     * @memberof InterviewFinding
     */
    evidence: Array<InterviewEvidence>;
}
/**
 *
 * @export
 * @interface InterviewMemoryCandidateRequest
 */
export interface InterviewMemoryCandidateRequest {
    /**
     *
     * @type {string}
     * @memberof InterviewMemoryCandidateRequest
     */
    workspace_id: string;
}
/**
 *
 * @export
 * @interface InterviewMemoryCandidateResult
 */
export interface InterviewMemoryCandidateResult {
    /**
     *
     * @type {string}
     * @memberof InterviewMemoryCandidateResult
     */
    memory_id: string;
    /**
     *
     * @type {boolean}
     * @memberof InterviewMemoryCandidateResult
     */
    replayed: boolean;
}
/**
 * Participant question projection. Canonical answer_points are deliberately not present.
 * @export
 * @interface InterviewQuestion
 */
export interface InterviewQuestion {
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    session_id: string;
    /**
     *
     * @type {number}
     * @memberof InterviewQuestion
     */
    question_no: number;
    /**
     *
     * @type {number}
     * @memberof InterviewQuestion
     */
    follow_up_no: number;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    parent_question_id?: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    topic_id?: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    prompt: string;
    /**
     *
     * @type {InterviewQuestionStatusEnum}
     * @memberof InterviewQuestion
     */
    status: InterviewQuestionStatusEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof InterviewQuestion
     */
    answered_at?: string;
}


/**
 * @export
 */
export const InterviewQuestionStatusEnum = {
    Pending: 'PENDING',
    Answered: 'ANSWERED',
    Skipped: 'SKIPPED'
} as const;
export type InterviewQuestionStatusEnum = typeof InterviewQuestionStatusEnum[keyof typeof InterviewQuestionStatusEnum];

/**
 *
 * @export
 * @interface InterviewReport
 */
export interface InterviewReport {
    /**
     *
     * @type {string}
     * @memberof InterviewReport
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewReport
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewReport
     */
    session_id: string;
    /**
     *
     * @type {InterviewReportSchemaVersionEnum}
     * @memberof InterviewReport
     */
    schema_version: InterviewReportSchemaVersionEnum;
    /**
     *
     * @type {InterviewReportSummary}
     * @memberof InterviewReport
     */
    summary: InterviewReportSummary;
    /**
     *
     * @type {Array<InterviewFinding>}
     * @memberof InterviewReport
     */
    strengths: Array<InterviewFinding>;
    /**
     *
     * @type {Array<InterviewFinding>}
     * @memberof InterviewReport
     */
    gaps: Array<InterviewFinding>;
    /**
     *
     * @type {Array<InterviewFinding>}
     * @memberof InterviewReport
     */
    expression: Array<InterviewFinding>;
    /**
     *
     * @type {Array<InterviewEvidence>}
     * @memberof InterviewReport
     */
    evidence: Array<InterviewEvidence>;
    /**
     *
     * @type {InterviewReportArtifactBinding}
     * @memberof InterviewReport
     */
    artifact: InterviewReportArtifactBinding;
    /**
     *
     * @type {string}
     * @memberof InterviewReport
     */
    created_at: string;
}


/**
 * @export
 */
export const InterviewReportSchemaVersionEnum = {
    InterviewReportV1: 'interview-report/v1'
} as const;
export type InterviewReportSchemaVersionEnum = typeof InterviewReportSchemaVersionEnum[keyof typeof InterviewReportSchemaVersionEnum];

/**
 *
 * @export
 * @interface InterviewReportArtifactBinding
 */
export interface InterviewReportArtifactBinding {
    /**
     *
     * @type {InterviewReportArtifactBindingKindEnum}
     * @memberof InterviewReportArtifactBinding
     */
    kind: InterviewReportArtifactBindingKindEnum;
    /**
     *
     * @type {string}
     * @memberof InterviewReportArtifactBinding
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewReportArtifactBinding
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof InterviewReportArtifactBinding
     */
    artifact_version: number;
}


/**
 * @export
 */
export const InterviewReportArtifactBindingKindEnum = {
    InterviewDoc: 'INTERVIEW_DOC'
} as const;
export type InterviewReportArtifactBindingKindEnum = typeof InterviewReportArtifactBindingKindEnum[keyof typeof InterviewReportArtifactBindingKindEnum];

/**
 *
 * @export
 * @interface InterviewReportSummary
 */
export interface InterviewReportSummary {
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    questions_total: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    answered_total: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    skipped_total: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    correctness: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    coverage: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    boundaries: number;
    /**
     *
     * @type {number}
     * @memberof InterviewReportSummary
     */
    clarity: number;
}
/**
 *
 * @export
 * @interface InterviewScope
 */
export interface InterviewScope {
    /**
     *
     * @type {Array<string>}
     * @memberof InterviewScope
     */
    claim_ids?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof InterviewScope
     */
    topic_ids?: Array<string>;
}
/**
 *
 * @export
 * @interface InterviewScore
 */
export interface InterviewScore {
    /**
     *
     * @type {InterviewScoreSchemaVersionEnum}
     * @memberof InterviewScore
     */
    schema_version: InterviewScoreSchemaVersionEnum;
    /**
     *
     * @type {InterviewScoreDimension}
     * @memberof InterviewScore
     */
    correctness: InterviewScoreDimension;
    /**
     *
     * @type {InterviewScoreDimension}
     * @memberof InterviewScore
     */
    coverage: InterviewScoreDimension;
    /**
     *
     * @type {InterviewScoreDimension}
     * @memberof InterviewScore
     */
    boundaries: InterviewScoreDimension;
    /**
     *
     * @type {InterviewScoreDimension}
     * @memberof InterviewScore
     */
    clarity: InterviewScoreDimension;
    /**
     *
     * @type {Array<string>}
     * @memberof InterviewScore
     */
    errors?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof InterviewScore
     */
    omissions?: Array<string>;
    /**
     *
     * @type {Array<InterviewEvidence>}
     * @memberof InterviewScore
     */
    evidence: Array<InterviewEvidence>;
}


/**
 * @export
 */
export const InterviewScoreSchemaVersionEnum = {
    InterviewScoreV1: 'interview-score/v1'
} as const;
export type InterviewScoreSchemaVersionEnum = typeof InterviewScoreSchemaVersionEnum[keyof typeof InterviewScoreSchemaVersionEnum];

/**
 *
 * @export
 * @interface InterviewScoreDimension
 */
export interface InterviewScoreDimension {
    /**
     *
     * @type {number}
     * @memberof InterviewScoreDimension
     */
    value: number;
    /**
     *
     * @type {string}
     * @memberof InterviewScoreDimension
     */
    rationale: string;
}
/**
 *
 * @export
 * @interface InterviewSession
 */
export interface InterviewSession {
    /**
     *
     * @type {string}
     * @memberof InterviewSession
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewSession
     */
    workspace_id: string;
    /**
     *
     * @type {InterviewConfig}
     * @memberof InterviewSession
     */
    config: InterviewConfig;
    /**
     *
     * @type {InterviewSessionStatusEnum}
     * @memberof InterviewSession
     */
    status: InterviewSessionStatusEnum;
    /**
     *
     * @type {number}
     * @memberof InterviewSession
     */
    version: number;
    /**
     *
     * @type {number}
     * @memberof InterviewSession
     */
    follow_up_count: number;
    /**
     *
     * @type {string}
     * @memberof InterviewSession
     */
    started_at: string;
    /**
     *
     * @type {string}
     * @memberof InterviewSession
     */
    ended_at?: string;
}


/**
 * @export
 */
export const InterviewSessionStatusEnum = {
    Active: 'ACTIVE',
    Completed: 'COMPLETED',
    Cancelled: 'CANCELLED'
} as const;
export type InterviewSessionStatusEnum = typeof InterviewSessionStatusEnum[keyof typeof InterviewSessionStatusEnum];

/**
 *
 * @export
 * @interface InterviewSessionPage
 */
export interface InterviewSessionPage {
    /**
     *
     * @type {string}
     * @memberof InterviewSessionPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<InterviewSession>}
     * @memberof InterviewSessionPage
     */
    items: Array<InterviewSession>;
    /**
     *
     * @type {string}
     * @memberof InterviewSessionPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface InterviewSnapshot
 */
export interface InterviewSnapshot {
    /**
     *
     * @type {InterviewSession}
     * @memberof InterviewSnapshot
     */
    session: InterviewSession;
    /**
     *
     * @type {Array<InterviewQuestion>}
     * @memberof InterviewSnapshot
     */
    questions: Array<InterviewQuestion>;
    /**
     *
     * @type {Array<InterviewTurn>}
     * @memberof InterviewSnapshot
     */
    turns: Array<InterviewTurn>;
    /**
     *
     * @type {InterviewReport}
     * @memberof InterviewSnapshot
     */
    report?: InterviewReport;
    /**
     *
     * @type {LearningPath}
     * @memberof InterviewSnapshot
     */
    path?: LearningPath;
    /**
     *
     * @type {Array<LearningPathStep>}
     * @memberof InterviewSnapshot
     */
    steps: Array<LearningPathStep>;
}
/**
 *
 * @export
 * @interface InterviewStartResult
 */
export interface InterviewStartResult {
    /**
     *
     * @type {InterviewSession}
     * @memberof InterviewStartResult
     */
    session: InterviewSession;
    /**
     *
     * @type {Array<InterviewQuestion>}
     * @memberof InterviewStartResult
     */
    questions: Array<InterviewQuestion>;
    /**
     *
     * @type {boolean}
     * @memberof InterviewStartResult
     */
    replayed: boolean;
}
/**
 * Raw user_answer and request receipt identity are deliberately excluded from recovery projections.
 * @export
 * @interface InterviewTurn
 */
export interface InterviewTurn {
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    session_id: string;
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    question_id: string;
    /**
     *
     * @type {InterviewScore}
     * @memberof InterviewTurn
     */
    score: InterviewScore;
    /**
     *
     * @type {InterviewTurnDecision}
     * @memberof InterviewTurn
     */
    decision: InterviewTurnDecision;
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    scorer_version: string;
    /**
     *
     * @type {string}
     * @memberof InterviewTurn
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface InterviewTurnDecision
 */
export interface InterviewTurnDecision {
    /**
     *
     * @type {boolean}
     * @memberof InterviewTurnDecision
     */
    follow_up_created: boolean;
    /**
     *
     * @type {string}
     * @memberof InterviewTurnDecision
     */
    follow_up_question_id?: string;
    /**
     *
     * @type {string}
     * @memberof InterviewTurnDecision
     */
    next_question_id?: string;
}
/**
 *
 * @export
 * @interface InterviewTurnResult
 */
export interface InterviewTurnResult {
    /**
     *
     * @type {InterviewTurn}
     * @memberof InterviewTurnResult
     */
    turn: InterviewTurn;
    /**
     *
     * @type {InterviewQuestion}
     * @memberof InterviewTurnResult
     */
    follow_up?: InterviewQuestion;
    /**
     *
     * @type {InterviewQuestion}
     * @memberof InterviewTurnResult
     */
    next_question?: InterviewQuestion;
    /**
     *
     * @type {boolean}
     * @memberof InterviewTurnResult
     */
    replayed: boolean;
}
/**
 * @type JSONValue
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type JSONValue = Array<JSONValue> | boolean | number | string | { [key: string]: JSONValue; } | null;

/**
 *
 * @export
 * @interface KnowledgeChangeBaseVersion
 */
export interface KnowledgeChangeBaseVersion {
    /**
     *
     * @type {SemanticLinkNodeType}
     * @memberof KnowledgeChangeBaseVersion
     */
    node_type: SemanticLinkNodeType;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeBaseVersion
     */
    node_id: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeChangeBaseVersion
     */
    version: number;
}


/**
 *
 * @export
 * @interface KnowledgeChangeEndpoint
 */
export interface KnowledgeChangeEndpoint {
    /**
     *
     * @type {SemanticLinkNodeType}
     * @memberof KnowledgeChangeEndpoint
     */
    type: SemanticLinkNodeType;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeEndpoint
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeChangeEndpoint
     */
    version: number;
}


/**
 *
 * @export
 * @interface KnowledgeChangeEvidenceRef
 */
export interface KnowledgeChangeEvidenceRef {
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeEvidenceRef
     */
    candidate_evidence_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeEvidenceRef
     */
    semantic_hash: string;
}
/**
 *
 * @export
 * @interface KnowledgeChangeProposal
 */
export interface KnowledgeChangeProposal {
    /**
     *
     * @type {KnowledgeChangeProposalProposalTypeEnum}
     * @memberof KnowledgeChangeProposal
     */
    proposal_type: KnowledgeChangeProposalProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeProposal
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeProposal
     */
    workspace_id: string;
    /**
     *
     * @type {KnowledgeChangeProposalStatusEnum}
     * @memberof KnowledgeChangeProposal
     */
    status: KnowledgeChangeProposalStatusEnum;
    /**
     *
     * @type {KnowledgeChangeProposalRiskLevelEnum}
     * @memberof KnowledgeChangeProposal
     */
    risk_level: KnowledgeChangeProposalRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof KnowledgeChangeProposal
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof KnowledgeChangeProposal
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {KnowledgeChangeRevision}
     * @memberof KnowledgeChangeProposal
     */
    revision: KnowledgeChangeRevision;
    /**
     *
     * @type {NonFileApproval}
     * @memberof KnowledgeChangeProposal
     */
    approval: NonFileApproval | null;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeProposal
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeProposal
     */
    updated_at: string;
}


/**
 * @export
 */
export const KnowledgeChangeProposalProposalTypeEnum = {
    KnowledgeChange: 'knowledge_change'
} as const;
export type KnowledgeChangeProposalProposalTypeEnum = typeof KnowledgeChangeProposalProposalTypeEnum[keyof typeof KnowledgeChangeProposalProposalTypeEnum];

/**
 * @export
 */
export const KnowledgeChangeProposalStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type KnowledgeChangeProposalStatusEnum = typeof KnowledgeChangeProposalStatusEnum[keyof typeof KnowledgeChangeProposalStatusEnum];

/**
 * @export
 */
export const KnowledgeChangeProposalRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type KnowledgeChangeProposalRiskLevelEnum = typeof KnowledgeChangeProposalRiskLevelEnum[keyof typeof KnowledgeChangeProposalRiskLevelEnum];

/**
 *
 * @export
 * @interface KnowledgeChangeRevision
 */
export interface KnowledgeChangeRevision {
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeChangeRevision
     */
    revision_no: number;
    /**
     *
     * @type {KnowledgeChangeRevisionSchemaVersionEnum}
     * @memberof KnowledgeChangeRevision
     */
    schema_version: KnowledgeChangeRevisionSchemaVersionEnum;
    /**
     *
     * @type {Array<KnowledgeChangeTargetRef>}
     * @memberof KnowledgeChangeRevision
     */
    target_refs: Array<KnowledgeChangeTargetRef>;
    /**
     *
     * @type {Array<KnowledgeChangeBaseVersion>}
     * @memberof KnowledgeChangeRevision
     */
    base_versions: Array<KnowledgeChangeBaseVersion>;
    /**
     *
     * @type {KnowledgeChangeSet}
     * @memberof KnowledgeChangeRevision
     */
    change_set: KnowledgeChangeSet;
    /**
     *
     * @type {Array<KnowledgeChangeEvidenceRef>}
     * @memberof KnowledgeChangeRevision
     */
    evidence_refs: Array<KnowledgeChangeEvidenceRef>;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const KnowledgeChangeRevisionSchemaVersionEnum = {
    KnowledgeRelationChangeV1: 'knowledge-relation-change/v1'
} as const;
export type KnowledgeChangeRevisionSchemaVersionEnum = typeof KnowledgeChangeRevisionSchemaVersionEnum[keyof typeof KnowledgeChangeRevisionSchemaVersionEnum];

/**
 *
 * @export
 * @interface KnowledgeChangeSet
 */
export interface KnowledgeChangeSet {
    /**
     *
     * @type {KnowledgeChangeSetOperationEnum}
     * @memberof KnowledgeChangeSet
     */
    operation: KnowledgeChangeSetOperationEnum;
    /**
     *
     * @type {KnowledgeChangeEndpoint}
     * @memberof KnowledgeChangeSet
     */
    source: KnowledgeChangeEndpoint;
    /**
     *
     * @type {KnowledgeChangeEndpoint}
     * @memberof KnowledgeChangeSet
     */
    target: KnowledgeChangeEndpoint;
    /**
     *
     * @type {SemanticLinkRelationType}
     * @memberof KnowledgeChangeSet
     */
    relation_type: SemanticLinkRelationType;
}


/**
 * @export
 */
export const KnowledgeChangeSetOperationEnum = {
    CreateRelation: 'CREATE_RELATION'
} as const;
export type KnowledgeChangeSetOperationEnum = typeof KnowledgeChangeSetOperationEnum[keyof typeof KnowledgeChangeSetOperationEnum];

/**
 *
 * @export
 * @interface KnowledgeChangeTargetRef
 */
export interface KnowledgeChangeTargetRef {
    /**
     *
     * @type {KnowledgeChangeTargetRefTypeEnum}
     * @memberof KnowledgeChangeTargetRef
     */
    type: KnowledgeChangeTargetRefTypeEnum;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeTargetRef
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeChangeTargetRef
     */
    fingerprint: string;
}


/**
 * @export
 */
export const KnowledgeChangeTargetRefTypeEnum = {
    RelationCandidate: 'RELATION_CANDIDATE'
} as const;
export type KnowledgeChangeTargetRefTypeEnum = typeof KnowledgeChangeTargetRefTypeEnum[keyof typeof KnowledgeChangeTargetRefTypeEnum];

/**
 * @type KnowledgeEvent
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type KnowledgeEvent = { schema_version: 'knowledge-event/v1' } & KnowledgeEventV1 | { schema_version: 'knowledge-event/v2' } & KnowledgeEventV2;

/**
 *
 * @export
 * @interface KnowledgeEventCorrelation
 */
export interface KnowledgeEventCorrelation {
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventCorrelation
     */
    proposal_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventCorrelation
     */
    approval_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventCorrelation
     */
    workflow_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventCorrelation
     */
    audit_event_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventCorrelation
     */
    git_commit_ref?: string;
}
/**
 *
 * @export
 * @interface KnowledgeEventOperator
 */
export interface KnowledgeEventOperator {
    /**
     *
     * @type {KnowledgeEventOperatorTypeEnum}
     * @memberof KnowledgeEventOperator
     */
    type: KnowledgeEventOperatorTypeEnum;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventOperator
     */
    id?: string;
}


/**
 * @export
 */
export const KnowledgeEventOperatorTypeEnum = {
    User: 'USER',
    ApiToken: 'API_TOKEN',
    System: 'SYSTEM',
    Unknown: 'UNKNOWN'
} as const;
export type KnowledgeEventOperatorTypeEnum = typeof KnowledgeEventOperatorTypeEnum[keyof typeof KnowledgeEventOperatorTypeEnum];

/**
 * @type KnowledgeEventOwnerBinding
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type KnowledgeEventOwnerBinding = ArtifactEventOwnerBinding | ReviewCardEventOwnerBinding;


/**
 *
 * @export
 */
export const KnowledgeEventType = {
    ProposalCreated: 'PROPOSAL_CREATED',
    ApprovalGranted: 'APPROVAL_GRANTED',
    ApprovalRejected: 'APPROVAL_REJECTED',
    GitCommitted: 'GIT_COMMITTED',
    RelationConfirmed: 'RELATION_CONFIRMED',
    RelationDeprecated: 'RELATION_DEPRECATED',
    ConflictOpened: 'CONFLICT_OPENED',
    ConflictTransitioned: 'CONFLICT_TRANSITIONED',
    ConflictResolved: 'CONFLICT_RESOLVED',
    VersionPublished: 'VERSION_PUBLISHED',
    VersionSuperseded: 'VERSION_SUPERSEDED',
    HealthIssueDetected: 'HEALTH_ISSUE_DETECTED',
    HealthIssueResolved: 'HEALTH_ISSUE_RESOLVED',
    ImpactAnalyzed: 'IMPACT_ANALYZED',
    ArtifactGenerated: 'ARTIFACT_GENERATED',
    ReviewCardInvalidated: 'REVIEW_CARD_INVALIDATED',
    CorrectiveEvent: 'CORRECTIVE_EVENT'
} as const;
export type KnowledgeEventType = typeof KnowledgeEventType[keyof typeof KnowledgeEventType];

/**
 *
 * @export
 * @interface KnowledgeEventV1
 */
export interface KnowledgeEventV1 {
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    workspace_id: string;
    /**
     *
     * @type {KnowledgeEventV1EventTypeEnum}
     * @memberof KnowledgeEventV1
     */
    event_type: KnowledgeEventV1EventTypeEnum;
    /**
     *
     * @type {TimelineAggregateType}
     * @memberof KnowledgeEventV1
     */
    aggregate_type: TimelineAggregateType;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    aggregate_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    source_event_ref: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    source_ref: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeEventV1
     */
    event_version: number;
    /**
     *
     * @type {KnowledgeEventV1SchemaVersionEnum}
     * @memberof KnowledgeEventV1
     */
    schema_version: KnowledgeEventV1SchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    summary: string;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof KnowledgeEventV1
     */
    payload: { [key: string]: JSONValue; };
    /**
     *
     * @type {KnowledgeEventCorrelation}
     * @memberof KnowledgeEventV1
     */
    correlation: KnowledgeEventCorrelation;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    occurred_at: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV1
     */
    created_at: string;
}


/**
 * @export
 */
export const KnowledgeEventV1EventTypeEnum = {
    ProposalCreated: 'PROPOSAL_CREATED',
    ApprovalGranted: 'APPROVAL_GRANTED',
    ApprovalRejected: 'APPROVAL_REJECTED',
    GitCommitted: 'GIT_COMMITTED',
    RelationConfirmed: 'RELATION_CONFIRMED',
    RelationDeprecated: 'RELATION_DEPRECATED',
    ConflictOpened: 'CONFLICT_OPENED',
    ConflictTransitioned: 'CONFLICT_TRANSITIONED',
    ConflictResolved: 'CONFLICT_RESOLVED',
    VersionPublished: 'VERSION_PUBLISHED',
    VersionSuperseded: 'VERSION_SUPERSEDED',
    HealthIssueDetected: 'HEALTH_ISSUE_DETECTED',
    HealthIssueResolved: 'HEALTH_ISSUE_RESOLVED',
    ImpactAnalyzed: 'IMPACT_ANALYZED',
    CorrectiveEvent: 'CORRECTIVE_EVENT'
} as const;
export type KnowledgeEventV1EventTypeEnum = typeof KnowledgeEventV1EventTypeEnum[keyof typeof KnowledgeEventV1EventTypeEnum];

/**
 * @export
 */
export const KnowledgeEventV1SchemaVersionEnum = {
    KnowledgeEventV1: 'knowledge-event/v1'
} as const;
export type KnowledgeEventV1SchemaVersionEnum = typeof KnowledgeEventV1SchemaVersionEnum[keyof typeof KnowledgeEventV1SchemaVersionEnum];

/**
 *
 * @export
 * @interface KnowledgeEventV2
 */
export interface KnowledgeEventV2 {
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    workspace_id: string;
    /**
     *
     * @type {KnowledgeEventType}
     * @memberof KnowledgeEventV2
     */
    event_type: KnowledgeEventType;
    /**
     *
     * @type {TimelineAggregateType}
     * @memberof KnowledgeEventV2
     */
    aggregate_type: TimelineAggregateType;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    aggregate_id?: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    source_event_ref: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    source_ref: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeEventV2
     */
    event_version: number;
    /**
     *
     * @type {KnowledgeEventV2SchemaVersionEnum}
     * @memberof KnowledgeEventV2
     */
    schema_version: KnowledgeEventV2SchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    summary: string;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof KnowledgeEventV2
     */
    payload: { [key: string]: JSONValue; };
    /**
     *
     * @type {KnowledgeEventCorrelation}
     * @memberof KnowledgeEventV2
     */
    correlation: KnowledgeEventCorrelation;
    /**
     *
     * @type {KnowledgeEventOperator}
     * @memberof KnowledgeEventV2
     */
    operator: KnowledgeEventOperator;
    /**
     *
     * @type {KnowledgeEventV2OwnerBinding}
     * @memberof KnowledgeEventV2
     */
    owner_binding: KnowledgeEventV2OwnerBinding;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    occurred_at: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeEventV2
     */
    created_at: string;
}


/**
 * @export
 */
export const KnowledgeEventV2SchemaVersionEnum = {
    KnowledgeEventV2: 'knowledge-event/v2'
} as const;
export type KnowledgeEventV2SchemaVersionEnum = typeof KnowledgeEventV2SchemaVersionEnum[keyof typeof KnowledgeEventV2SchemaVersionEnum];

/**
 * @type KnowledgeEventV2OwnerBinding
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type KnowledgeEventV2OwnerBinding = KnowledgeEventOwnerBinding | null;

/**
 *
 * @export
 * @interface KnowledgeProfileCandidate
 */
export interface KnowledgeProfileCandidate {
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileCandidate
     */
    label: string;
    /**
     *
     * @type {Array<string>}
     * @memberof KnowledgeProfileCandidate
     */
    aliases?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof KnowledgeProfileCandidate
     */
    source_span_ids: Array<string>;
}
/**
 *
 * @export
 * @interface KnowledgeProfileCommandResult
 */
export interface KnowledgeProfileCommandResult {
    /**
     *
     * @type {DocumentKnowledgeProfile}
     * @memberof KnowledgeProfileCommandResult
     */
    profile: DocumentKnowledgeProfile;
    /**
     *
     * @type {KnowledgeProfileRevision}
     * @memberof KnowledgeProfileCommandResult
     */
    revision: KnowledgeProfileRevision | null;
    /**
     *
     * @type {boolean}
     * @memberof KnowledgeProfileCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface KnowledgeProfilePoint
 */
export interface KnowledgeProfilePoint {
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfilePoint
     */
    text: string;
    /**
     *
     * @type {Array<string>}
     * @memberof KnowledgeProfilePoint
     */
    source_span_ids: Array<string>;
}
/**
 *
 * @export
 * @interface KnowledgeProfileRevision
 */
export interface KnowledgeProfileRevision {
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    profile_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    parse_projection_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    model_run_id: string;
    /**
     *
     * @type {number}
     * @memberof KnowledgeProfileRevision
     */
    model_settings_revision: number | null;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    prompt_version: string;
    /**
     *
     * @type {KnowledgeProfileRevisionSchemaVersionEnum}
     * @memberof KnowledgeProfileRevision
     */
    schema_version: KnowledgeProfileRevisionSchemaVersionEnum;
    /**
     *
     * @type {DocumentKnowledgeProfileContent}
     * @memberof KnowledgeProfileRevision
     */
    content: DocumentKnowledgeProfileContent;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    content_digest: string;
    /**
     *
     * @type {string}
     * @memberof KnowledgeProfileRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const KnowledgeProfileRevisionSchemaVersionEnum = {
    DocumentKnowledgeProfileV1: 'document-knowledge-profile/v1'
} as const;
export type KnowledgeProfileRevisionSchemaVersionEnum = typeof KnowledgeProfileRevisionSchemaVersionEnum[keyof typeof KnowledgeProfileRevisionSchemaVersionEnum];


/**
 *
 * @export
 */
export const KnowledgeProfileStatus = {
    Pending: 'PENDING',
    Running: 'RUNNING',
    Ready: 'READY',
    Failed: 'FAILED',
    CapabilityUnavailable: 'CAPABILITY_UNAVAILABLE',
    Stale: 'STALE'
} as const;
export type KnowledgeProfileStatus = typeof KnowledgeProfileStatus[keyof typeof KnowledgeProfileStatus];

/**
 *
 * @export
 * @interface KnowledgeTimelinePage
 */
export interface KnowledgeTimelinePage {
    /**
     *
     * @type {string}
     * @memberof KnowledgeTimelinePage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<KnowledgeEvent>}
     * @memberof KnowledgeTimelinePage
     */
    items: Array<KnowledgeEvent>;
    /**
     * Opaque HMAC cursor bound to Workspace, canonical filters and page size.
     * @type {string}
     * @memberof KnowledgeTimelinePage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface LearningPath
 */
export interface LearningPath {
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    session_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    report_id: string;
    /**
     *
     * @type {LearningPathArtifactBinding}
     * @memberof LearningPath
     */
    artifact: LearningPathArtifactBinding;
    /**
     *
     * @type {LearningPathStatusEnum}
     * @memberof LearningPath
     */
    status: LearningPathStatusEnum;
    /**
     *
     * @type {number}
     * @memberof LearningPath
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof LearningPath
     */
    updated_at: string;
}


/**
 * @export
 */
export const LearningPathStatusEnum = {
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Completed: 'COMPLETED'
} as const;
export type LearningPathStatusEnum = typeof LearningPathStatusEnum[keyof typeof LearningPathStatusEnum];

/**
 *
 * @export
 * @interface LearningPathArtifactBinding
 */
export interface LearningPathArtifactBinding {
    /**
     *
     * @type {LearningPathArtifactBindingKindEnum}
     * @memberof LearningPathArtifactBinding
     */
    kind: LearningPathArtifactBindingKindEnum;
    /**
     *
     * @type {string}
     * @memberof LearningPathArtifactBinding
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathArtifactBinding
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof LearningPathArtifactBinding
     */
    artifact_version: number;
}


/**
 * @export
 */
export const LearningPathArtifactBindingKindEnum = {
    LearningPath: 'LEARNING_PATH'
} as const;
export type LearningPathArtifactBindingKindEnum = typeof LearningPathArtifactBindingKindEnum[keyof typeof LearningPathArtifactBindingKindEnum];

/**
 *
 * @export
 * @interface LearningPathStatusResult
 */
export interface LearningPathStatusResult {
    /**
     *
     * @type {LearningPath}
     * @memberof LearningPathStatusResult
     */
    path: LearningPath;
    /**
     *
     * @type {boolean}
     * @memberof LearningPathStatusResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface LearningPathStep
 */
export interface LearningPathStep {
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    path_id: string;
    /**
     *
     * @type {number}
     * @memberof LearningPathStep
     */
    step_no: number;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    topic_id?: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    evidence_hash: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    rationale: string;
    /**
     *
     * @type {LearningPathStepStatusEnum}
     * @memberof LearningPathStep
     */
    status: LearningPathStepStatusEnum;
    /**
     *
     * @type {number}
     * @memberof LearningPathStep
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof LearningPathStep
     */
    updated_at: string;
}


/**
 * @export
 */
export const LearningPathStepStatusEnum = {
    Pending: 'PENDING',
    InProgress: 'IN_PROGRESS',
    Completed: 'COMPLETED',
    Skipped: 'SKIPPED'
} as const;
export type LearningPathStepStatusEnum = typeof LearningPathStepStatusEnum[keyof typeof LearningPathStepStatusEnum];

/**
 *
 * @export
 * @interface LearningPathStepResult
 */
export interface LearningPathStepResult {
    /**
     *
     * @type {LearningPath}
     * @memberof LearningPathStepResult
     */
    path: LearningPath;
    /**
     *
     * @type {LearningPathStep}
     * @memberof LearningPathStepResult
     */
    step: LearningPathStep;
    /**
     *
     * @type {boolean}
     * @memberof LearningPathStepResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface LexicalScore
 */
export interface LexicalScore {
    /**
     *
     * @type {number}
     * @memberof LexicalScore
     */
    rank: number;
    /**
     *
     * @type {number}
     * @memberof LexicalScore
     */
    score: number;
    /**
     *
     * @type {number}
     * @memberof LexicalScore
     */
    fts_score: number;
    /**
     *
     * @type {number}
     * @memberof LexicalScore
     */
    trigram_score: number;
}
/**
 *
 * @export
 * @interface Liveness
 */
export interface Liveness {
    /**
     *
     * @type {LivenessStatusEnum}
     * @memberof Liveness
     */
    status: LivenessStatusEnum;
}


/**
 * @export
 */
export const LivenessStatusEnum = {
    Alive: 'alive'
} as const;
export type LivenessStatusEnum = typeof LivenessStatusEnum[keyof typeof LivenessStatusEnum];

/**
 * Owner and confirmation-principal identities are intentionally omitted from the user-facing Memory wire model.
 * @export
 * @interface Memory
 */
export interface Memory {
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    workspace_id: string;
    /**
     *
     * @type {MemoryTypeEnum}
     * @memberof Memory
     */
    type: MemoryTypeEnum;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof Memory
     */
    content: { [key: string]: JSONValue; };
    /**
     *
     * @type {MemorySource}
     * @memberof Memory
     */
    source: MemorySource;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    task_scope_id?: string;
    /**
     *
     * @type {MemoryStatusEnum}
     * @memberof Memory
     */
    status: MemoryStatusEnum;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    expires_at?: string;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    confirmed_at?: string;
    /**
     *
     * @type {number}
     * @memberof Memory
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Memory
     */
    updated_at: string;
}


/**
 * @export
 */
export const MemoryTypeEnum = {
    Preference: 'PREFERENCE',
    Episodic: 'EPISODIC',
    Goal: 'GOAL',
    Feedback: 'FEEDBACK'
} as const;
export type MemoryTypeEnum = typeof MemoryTypeEnum[keyof typeof MemoryTypeEnum];

/**
 * @export
 */
export const MemoryStatusEnum = {
    Candidate: 'CANDIDATE',
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Expired: 'EXPIRED',
    Deleted: 'DELETED'
} as const;
export type MemoryStatusEnum = typeof MemoryStatusEnum[keyof typeof MemoryStatusEnum];

/**
 *
 * @export
 * @interface MemoryCommandResult
 */
export interface MemoryCommandResult {
    /**
     *
     * @type {Memory}
     * @memberof MemoryCommandResult
     */
    memory: Memory;
    /**
     *
     * @type {boolean}
     * @memberof MemoryCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface MemoryPage
 */
export interface MemoryPage {
    /**
     *
     * @type {string}
     * @memberof MemoryPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<Memory>}
     * @memberof MemoryPage
     */
    items: Array<Memory>;
    /**
     *
     * @type {string}
     * @memberof MemoryPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface MemorySource
 */
export interface MemorySource {
    /**
     *
     * @type {MemorySourceTypeEnum}
     * @memberof MemorySource
     */
    type: MemorySourceTypeEnum;
    /**
     *
     * @type {string}
     * @memberof MemorySource
     */
    ref: string;
}


/**
 * @export
 */
export const MemorySourceTypeEnum = {
    User: 'USER',
    Agent: 'AGENT',
    Interview: 'INTERVIEW'
} as const;
export type MemorySourceTypeEnum = typeof MemorySourceTypeEnum[keyof typeof MemorySourceTypeEnum];

/**
 *
 * @export
 * @interface MemoryTransitionRequest
 */
export interface MemoryTransitionRequest {
    /**
     *
     * @type {string}
     * @memberof MemoryTransitionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof MemoryTransitionRequest
     */
    expected_version: number;
}
/**
 * @type ModelAPIKeyAction
 * Explicit API-key operation. keep is valid only when provider and normalized base_url still match the stored desired key binding; replace supplies a new transient value; clear removes stored key material.
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelAPIKeyAction = { action: 'clear' } & ModelAPIKeyClear | { action: 'keep' } & ModelAPIKeyKeep | { action: 'replace' } & ModelAPIKeyReplace;

/**
 *
 * @export
 * @interface ModelAPIKeyClear
 */
export interface ModelAPIKeyClear {
    /**
     *
     * @type {ModelAPIKeyClearActionEnum}
     * @memberof ModelAPIKeyClear
     */
    action: ModelAPIKeyClearActionEnum;
}


/**
 * @export
 */
export const ModelAPIKeyClearActionEnum = {
    Clear: 'clear'
} as const;
export type ModelAPIKeyClearActionEnum = typeof ModelAPIKeyClearActionEnum[keyof typeof ModelAPIKeyClearActionEnum];

/**
 *
 * @export
 * @interface ModelAPIKeyKeep
 */
export interface ModelAPIKeyKeep {
    /**
     *
     * @type {ModelAPIKeyKeepActionEnum}
     * @memberof ModelAPIKeyKeep
     */
    action: ModelAPIKeyKeepActionEnum;
}


/**
 * @export
 */
export const ModelAPIKeyKeepActionEnum = {
    Keep: 'keep'
} as const;
export type ModelAPIKeyKeepActionEnum = typeof ModelAPIKeyKeepActionEnum[keyof typeof ModelAPIKeyKeepActionEnum];

/**
 *
 * @export
 * @interface ModelAPIKeyReplace
 */
export interface ModelAPIKeyReplace {
    /**
     *
     * @type {ModelAPIKeyReplaceActionEnum}
     * @memberof ModelAPIKeyReplace
     */
    action: ModelAPIKeyReplaceActionEnum;
    /**
     * Transient replacement value. It is never returned by any response.
     * @type {string}
     * @memberof ModelAPIKeyReplace
     */
    value: string;
}


/**
 * @export
 */
export const ModelAPIKeyReplaceActionEnum = {
    Replace: 'replace'
} as const;
export type ModelAPIKeyReplaceActionEnum = typeof ModelAPIKeyReplaceActionEnum[keyof typeof ModelAPIKeyReplaceActionEnum];


/**
 *
 * @export
 */
export const ModelCapabilityState = {
    Disabled: 'disabled',
    Configured: 'configured',
    Unavailable: 'unavailable'
} as const;
export type ModelCapabilityState = typeof ModelCapabilityState[keyof typeof ModelCapabilityState];

/**
 * @type ModelChatSettingsDraft
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelChatSettingsDraft = DisabledChatDraft | OllamaChatDraft | OpenAICompatibleChatDraft;

/**
 * @type ModelChatSettingsSummary
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelChatSettingsSummary = DisabledChatSettings | OllamaChatSettings | OpenAICompatibleChatSettings;

/**
 * @type ModelEmbeddingSettingsDraft
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelEmbeddingSettingsDraft = DisabledEmbeddingDraft | OllamaEmbeddingDraft | OpenAICompatibleEmbeddingDraft;

/**
 * @type ModelEmbeddingSettingsSummary
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelEmbeddingSettingsSummary = DisabledEmbeddingSettings | OllamaEmbeddingSettings | OpenAICompatibleEmbeddingSettings;

/**
 *
 * @export
 * @interface ModelLocalRuntime
 */
export interface ModelLocalRuntime {
    /**
     *
     * @type {ModelLocalRuntimeModeEnum}
     * @memberof ModelLocalRuntime
     */
    mode: ModelLocalRuntimeModeEnum;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    phase: string;
    /**
     *
     * @type {boolean}
     * @memberof ModelLocalRuntime
     */
    fresh: boolean;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    requirement_hash: string;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    ready_hash: string;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    operation_id: string | null;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    operation_phase: string | null;
    /**
     *
     * @type {number}
     * @memberof ModelLocalRuntime
     */
    completed_bytes: number;
    /**
     *
     * @type {number}
     * @memberof ModelLocalRuntime
     */
    total_bytes: number | null;
    /**
     *
     * @type {boolean}
     * @memberof ModelLocalRuntime
     */
    progress_known: boolean;
    /**
     *
     * @type {string}
     * @memberof ModelLocalRuntime
     */
    operation_error: string | null;
    /**
     *
     * @type {boolean}
     * @memberof ModelLocalRuntime
     */
    operation_retryable: boolean;
}


/**
 * @export
 */
export const ModelLocalRuntimeModeEnum = {
    Empty: '',
    Managed: 'managed',
    ExternalStatic: 'external-static'
} as const;
export type ModelLocalRuntimeModeEnum = typeof ModelLocalRuntimeModeEnum[keyof typeof ModelLocalRuntimeModeEnum];

/**
 *
 * @export
 * @interface ModelSettingsCapabilities
 */
export interface ModelSettingsCapabilities {
    /**
     *
     * @type {ModelCapabilityState}
     * @memberof ModelSettingsCapabilities
     */
    chat: ModelCapabilityState;
    /**
     *
     * @type {ModelCapabilityState}
     * @memberof ModelSettingsCapabilities
     */
    embedding: ModelCapabilityState;
}


/**
 *
 * @export
 * @interface ModelSettingsConflictDetails
 */
export interface ModelSettingsConflictDetails {
    /**
     *
     * @type {number}
     * @memberof ModelSettingsConflictDetails
     */
    current_revision: number;
}
/**
 *
 * @export
 * @interface ModelSettingsConflictProblem
 */
export interface ModelSettingsConflictProblem {
    /**
     *
     * @type {string}
     * @memberof ModelSettingsConflictProblem
     */
    error_code: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsConflictProblem
     */
    message: string;
    /**
     *
     * @type {boolean}
     * @memberof ModelSettingsConflictProblem
     */
    retryable: boolean;
    /**
     *
     * @type {ModelSettingsConflictDetails}
     * @memberof ModelSettingsConflictProblem
     */
    details: ModelSettingsConflictDetails;
}
/**
 * @type ModelSettingsParticipant
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelSettingsParticipant = ModelSettingsParticipantOneOf | ModelSettingsParticipantOneOf1 | ModelSettingsParticipantOneOf2;

/**
 *
 * @export
 * @interface ModelSettingsParticipantOneOf
 */
export interface ModelSettingsParticipantOneOf {
    /**
     *
     * @type {ModelSettingsParticipantOneOfPresentEnum}
     * @memberof ModelSettingsParticipantOneOf
     */
    present?: ModelSettingsParticipantOneOfPresentEnum;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsParticipantOneOf
     */
    target_revision?: null;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsParticipantOneOf
     */
    phase?: null;
    /**
     *
     * @type {ModelSettingsParticipantOneOfFreshEnum}
     * @memberof ModelSettingsParticipantOneOf
     */
    fresh?: ModelSettingsParticipantOneOfFreshEnum;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsParticipantOneOf
     */
    last_error_code?: null;
    /**
     *
     * @type {ModelSettingsParticipantOneOfRetryableEnum}
     * @memberof ModelSettingsParticipantOneOf
     */
    retryable?: ModelSettingsParticipantOneOfRetryableEnum;
}


/**
 * @export
 */
export const ModelSettingsParticipantOneOfPresentEnum = {
    False: false
} as const;
export type ModelSettingsParticipantOneOfPresentEnum = typeof ModelSettingsParticipantOneOfPresentEnum[keyof typeof ModelSettingsParticipantOneOfPresentEnum];

/**
 * @export
 */
export const ModelSettingsParticipantOneOfFreshEnum = {
    False: false
} as const;
export type ModelSettingsParticipantOneOfFreshEnum = typeof ModelSettingsParticipantOneOfFreshEnum[keyof typeof ModelSettingsParticipantOneOfFreshEnum];

/**
 * @export
 */
export const ModelSettingsParticipantOneOfRetryableEnum = {
    False: false
} as const;
export type ModelSettingsParticipantOneOfRetryableEnum = typeof ModelSettingsParticipantOneOfRetryableEnum[keyof typeof ModelSettingsParticipantOneOfRetryableEnum];

/**
 *
 * @export
 * @interface ModelSettingsParticipantOneOf1
 */
export interface ModelSettingsParticipantOneOf1 {
    /**
     *
     * @type {ModelSettingsParticipantOneOf1PresentEnum}
     * @memberof ModelSettingsParticipantOneOf1
     */
    present?: ModelSettingsParticipantOneOf1PresentEnum;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsParticipantOneOf1
     */
    target_revision?: number;
    /**
     *
     * @type {ModelSettingsParticipantOneOf1PhaseEnum}
     * @memberof ModelSettingsParticipantOneOf1
     */
    phase?: ModelSettingsParticipantOneOf1PhaseEnum;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsParticipantOneOf1
     */
    last_error_code?: null;
    /**
     *
     * @type {ModelSettingsParticipantOneOf1RetryableEnum}
     * @memberof ModelSettingsParticipantOneOf1
     */
    retryable?: ModelSettingsParticipantOneOf1RetryableEnum;
}


/**
 * @export
 */
export const ModelSettingsParticipantOneOf1PresentEnum = {
    True: true
} as const;
export type ModelSettingsParticipantOneOf1PresentEnum = typeof ModelSettingsParticipantOneOf1PresentEnum[keyof typeof ModelSettingsParticipantOneOf1PresentEnum];

/**
 * @export
 */
export const ModelSettingsParticipantOneOf1PhaseEnum = {
    Preparing: 'preparing',
    Prepared: 'prepared',
    Armed: 'armed',
    Activated: 'activated',
    Aborted: 'aborted',
    Retired: 'retired'
} as const;
export type ModelSettingsParticipantOneOf1PhaseEnum = typeof ModelSettingsParticipantOneOf1PhaseEnum[keyof typeof ModelSettingsParticipantOneOf1PhaseEnum];

/**
 * @export
 */
export const ModelSettingsParticipantOneOf1RetryableEnum = {
    False: false
} as const;
export type ModelSettingsParticipantOneOf1RetryableEnum = typeof ModelSettingsParticipantOneOf1RetryableEnum[keyof typeof ModelSettingsParticipantOneOf1RetryableEnum];

/**
 *
 * @export
 * @interface ModelSettingsParticipantOneOf2
 */
export interface ModelSettingsParticipantOneOf2 {
    /**
     *
     * @type {ModelSettingsParticipantOneOf2PresentEnum}
     * @memberof ModelSettingsParticipantOneOf2
     */
    present?: ModelSettingsParticipantOneOf2PresentEnum;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsParticipantOneOf2
     */
    target_revision?: number;
    /**
     *
     * @type {ModelSettingsParticipantOneOf2PhaseEnum}
     * @memberof ModelSettingsParticipantOneOf2
     */
    phase?: ModelSettingsParticipantOneOf2PhaseEnum;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsParticipantOneOf2
     */
    last_error_code?: string;
}


/**
 * @export
 */
export const ModelSettingsParticipantOneOf2PresentEnum = {
    True: true
} as const;
export type ModelSettingsParticipantOneOf2PresentEnum = typeof ModelSettingsParticipantOneOf2PresentEnum[keyof typeof ModelSettingsParticipantOneOf2PresentEnum];

/**
 * @export
 */
export const ModelSettingsParticipantOneOf2PhaseEnum = {
    Failed: 'failed'
} as const;
export type ModelSettingsParticipantOneOf2PhaseEnum = typeof ModelSettingsParticipantOneOf2PhaseEnum[keyof typeof ModelSettingsParticipantOneOf2PhaseEnum];

/**
 *
 * @export
 * @interface ModelSettingsParticipants
 */
export interface ModelSettingsParticipants {
    /**
     *
     * @type {ModelSettingsParticipant}
     * @memberof ModelSettingsParticipants
     */
    api: ModelSettingsParticipant;
    /**
     *
     * @type {ModelSettingsParticipant}
     * @memberof ModelSettingsParticipants
     */
    worker: ModelSettingsParticipant;
}
/**
 *
 * @export
 * @interface ModelSettingsResponse
 */
export interface ModelSettingsResponse {
    /**
     * Latest immutable revision saved by the user. Revision zero is the canonical disabled bootstrap.
     * @type {number}
     * @memberof ModelSettingsResponse
     */
    desired_revision: number;
    /**
     * Globally committed revision that ordinary API and Worker processes must load.
     * @type {number}
     * @memberof ModelSettingsResponse
     */
    active_revision: number;
    /**
     *
     * @type {ModelSettingsSummary}
     * @memberof ModelSettingsResponse
     */
    desired_settings: ModelSettingsSummary;
    /**
     *
     * @type {ModelSettingsSummary}
     * @memberof ModelSettingsResponse
     */
    active_settings: ModelSettingsSummary;
    /**
     *
     * @type {ModelSettingsRuntime}
     * @memberof ModelSettingsResponse
     */
    runtime: ModelSettingsRuntime;
    /**
     *
     * @type {ModelSettingsRollout}
     * @memberof ModelSettingsResponse
     */
    rollout: ModelSettingsRollout;
    /**
     *
     * @type {ModelSettingsParticipants}
     * @memberof ModelSettingsResponse
     */
    participants: ModelSettingsParticipants;
    /**
     * True when desired differs from active, an activation is live, or either serving role is not fresh and active on the committed revision.
     * @type {boolean}
     * @memberof ModelSettingsResponse
     */
    apply_required: boolean;
    /**
     * Deprecated compatibility field. Ordinary model activation occurs in process and never requires a container restart.
     * @type {ModelSettingsResponseRestartRequiredEnum}
     * @memberof ModelSettingsResponse
     * @deprecated
     */
    restart_required: ModelSettingsResponseRestartRequiredEnum;
    /**
     *
     * @type {ModelSettingsCapabilities}
     * @memberof ModelSettingsResponse
     */
    capabilities: ModelSettingsCapabilities;
    /**
     *
     * @type {ModelLocalRuntime}
     * @memberof ModelSettingsResponse
     */
    local_runtime: ModelLocalRuntime;
}


/**
 * @export
 */
export const ModelSettingsResponseRestartRequiredEnum = {
    False: false
} as const;
export type ModelSettingsResponseRestartRequiredEnum = typeof ModelSettingsResponseRestartRequiredEnum[keyof typeof ModelSettingsResponseRestartRequiredEnum];

/**
 * @type ModelSettingsRollout
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelSettingsRollout = ModelSettingsRolloutOneOf | ModelSettingsRolloutOneOf1 | ModelSettingsRolloutOneOf2;

/**
 *
 * @export
 * @interface ModelSettingsRolloutOneOf
 */
export interface ModelSettingsRolloutOneOf {
    /**
     *
     * @type {null}
     * @memberof ModelSettingsRolloutOneOf
     */
    id?: null;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRolloutOneOf
     */
    version?: number;
    /**
     *
     * @type {ModelSettingsRolloutOneOfPhaseEnum}
     * @memberof ModelSettingsRolloutOneOf
     */
    phase?: ModelSettingsRolloutOneOfPhaseEnum;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsRolloutOneOf
     */
    target_revision?: null;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsRolloutOneOf
     */
    last_error_code?: null;
    /**
     *
     * @type {ModelSettingsRolloutOneOfRetryableEnum}
     * @memberof ModelSettingsRolloutOneOf
     */
    retryable?: ModelSettingsRolloutOneOfRetryableEnum;
}


/**
 * @export
 */
export const ModelSettingsRolloutOneOfPhaseEnum = {
    Idle: 'idle'
} as const;
export type ModelSettingsRolloutOneOfPhaseEnum = typeof ModelSettingsRolloutOneOfPhaseEnum[keyof typeof ModelSettingsRolloutOneOfPhaseEnum];

/**
 * @export
 */
export const ModelSettingsRolloutOneOfRetryableEnum = {
    False: false
} as const;
export type ModelSettingsRolloutOneOfRetryableEnum = typeof ModelSettingsRolloutOneOfRetryableEnum[keyof typeof ModelSettingsRolloutOneOfRetryableEnum];

/**
 *
 * @export
 * @interface ModelSettingsRolloutOneOf1
 */
export interface ModelSettingsRolloutOneOf1 {
    /**
     *
     * @type {string}
     * @memberof ModelSettingsRolloutOneOf1
     */
    id?: string;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRolloutOneOf1
     */
    version?: number;
    /**
     *
     * @type {ModelSettingsRolloutOneOf1PhaseEnum}
     * @memberof ModelSettingsRolloutOneOf1
     */
    phase?: ModelSettingsRolloutOneOf1PhaseEnum;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRolloutOneOf1
     */
    target_revision?: number;
    /**
     *
     * @type {null}
     * @memberof ModelSettingsRolloutOneOf1
     */
    last_error_code?: null;
    /**
     *
     * @type {ModelSettingsRolloutOneOf1RetryableEnum}
     * @memberof ModelSettingsRolloutOneOf1
     */
    retryable?: ModelSettingsRolloutOneOf1RetryableEnum;
}


/**
 * @export
 */
export const ModelSettingsRolloutOneOf1PhaseEnum = {
    Preparing: 'preparing',
    Arming: 'arming',
    Activating: 'activating'
} as const;
export type ModelSettingsRolloutOneOf1PhaseEnum = typeof ModelSettingsRolloutOneOf1PhaseEnum[keyof typeof ModelSettingsRolloutOneOf1PhaseEnum];

/**
 * @export
 */
export const ModelSettingsRolloutOneOf1RetryableEnum = {
    False: false
} as const;
export type ModelSettingsRolloutOneOf1RetryableEnum = typeof ModelSettingsRolloutOneOf1RetryableEnum[keyof typeof ModelSettingsRolloutOneOf1RetryableEnum];

/**
 *
 * @export
 * @interface ModelSettingsRolloutOneOf2
 */
export interface ModelSettingsRolloutOneOf2 {
    /**
     *
     * @type {string}
     * @memberof ModelSettingsRolloutOneOf2
     */
    id?: string;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRolloutOneOf2
     */
    version?: number;
    /**
     *
     * @type {ModelSettingsRolloutOneOf2PhaseEnum}
     * @memberof ModelSettingsRolloutOneOf2
     */
    phase?: ModelSettingsRolloutOneOf2PhaseEnum;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRolloutOneOf2
     */
    target_revision?: number;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsRolloutOneOf2
     */
    last_error_code?: string;
    /**
     *
     * @type {ModelSettingsRolloutOneOf2RetryableEnum}
     * @memberof ModelSettingsRolloutOneOf2
     */
    retryable?: ModelSettingsRolloutOneOf2RetryableEnum;
}


/**
 * @export
 */
export const ModelSettingsRolloutOneOf2PhaseEnum = {
    Failed: 'failed'
} as const;
export type ModelSettingsRolloutOneOf2PhaseEnum = typeof ModelSettingsRolloutOneOf2PhaseEnum[keyof typeof ModelSettingsRolloutOneOf2PhaseEnum];

/**
 * @export
 */
export const ModelSettingsRolloutOneOf2RetryableEnum = {
    True: true
} as const;
export type ModelSettingsRolloutOneOf2RetryableEnum = typeof ModelSettingsRolloutOneOf2RetryableEnum[keyof typeof ModelSettingsRolloutOneOf2RetryableEnum];

/**
 *
 * @export
 * @interface ModelSettingsRuntime
 */
export interface ModelSettingsRuntime {
    /**
     *
     * @type {ModelSettingsRuntimeRole}
     * @memberof ModelSettingsRuntime
     */
    api: ModelSettingsRuntimeRole;
    /**
     *
     * @type {ModelSettingsRuntimeRole}
     * @memberof ModelSettingsRuntime
     */
    worker: ModelSettingsRuntimeRole;
}
/**
 *
 * @export
 * @interface ModelSettingsRuntimeRole
 */
export interface ModelSettingsRuntimeRole {
    /**
     *
     * @type {number}
     * @memberof ModelSettingsRuntimeRole
     */
    applied_revision: number;
    /**
     *
     * @type {ModelSettingsRuntimeRolePhaseEnum}
     * @memberof ModelSettingsRuntimeRole
     */
    phase: ModelSettingsRuntimeRolePhaseEnum;
    /**
     *
     * @type {boolean}
     * @memberof ModelSettingsRuntimeRole
     */
    fresh: boolean;
}


/**
 * @export
 */
export const ModelSettingsRuntimeRolePhaseEnum = {
    Active: 'active',
    Unavailable: 'unavailable'
} as const;
export type ModelSettingsRuntimeRolePhaseEnum = typeof ModelSettingsRuntimeRolePhaseEnum[keyof typeof ModelSettingsRuntimeRolePhaseEnum];

/**
 *
 * @export
 * @interface ModelSettingsSummary
 */
export interface ModelSettingsSummary {
    /**
     *
     * @type {ModelChatSettingsSummary}
     * @memberof ModelSettingsSummary
     */
    chat: ModelChatSettingsSummary;
    /**
     *
     * @type {ModelEmbeddingSettingsSummary}
     * @memberof ModelSettingsSummary
     */
    embedding: ModelEmbeddingSettingsSummary;
}
/**
 *
 * @export
 * @interface ModelSettingsTestDetails
 */
export interface ModelSettingsTestDetails {
    /**
     *
     * @type {ModelSettingsTestDetailsTargetEnum}
     * @memberof ModelSettingsTestDetails
     */
    target: ModelSettingsTestDetailsTargetEnum;
    /**
     *
     * @type {ModelSettingsTestDetailsStageEnum}
     * @memberof ModelSettingsTestDetails
     */
    stage: ModelSettingsTestDetailsStageEnum;
    /**
     *
     * @type {number}
     * @memberof ModelSettingsTestDetails
     */
    provider_http_status?: number;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestDetails
     */
    provider_error_code?: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestDetails
     */
    provider_error_type?: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestDetails
     */
    provider_message?: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestDetails
     */
    provider_request_id?: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestDetails
     */
    transport_error?: string;
    /**
     *
     * @type {ModelSettingsTestDetailsValidationReasonEnum}
     * @memberof ModelSettingsTestDetails
     */
    validation_reason?: ModelSettingsTestDetailsValidationReasonEnum;
}


/**
 * @export
 */
export const ModelSettingsTestDetailsTargetEnum = {
    Chat: 'chat',
    Embedding: 'embedding'
} as const;
export type ModelSettingsTestDetailsTargetEnum = typeof ModelSettingsTestDetailsTargetEnum[keyof typeof ModelSettingsTestDetailsTargetEnum];

/**
 * @export
 */
export const ModelSettingsTestDetailsStageEnum = {
    Request: 'request',
    Dns: 'dns',
    Connect: 'connect',
    Tls: 'tls',
    ProviderResponse: 'provider_response',
    ResponseRead: 'response_read',
    ResponseValidation: 'response_validation',
    Cancelled: 'cancelled',
    Timeout: 'timeout'
} as const;
export type ModelSettingsTestDetailsStageEnum = typeof ModelSettingsTestDetailsStageEnum[keyof typeof ModelSettingsTestDetailsStageEnum];

/**
 * @export
 */
export const ModelSettingsTestDetailsValidationReasonEnum = {
    InvalidResponse: 'invalid_response',
    ModelMismatch: 'model_mismatch',
    FinishReasonLength: 'finish_reason_length',
    FinishReasonInvalid: 'finish_reason_invalid',
    EmptyContent: 'empty_content',
    Refusal: 'refusal',
    ToolCalls: 'tool_calls',
    MissingUsage: 'missing_usage',
    InvalidUsage: 'invalid_usage',
    ResponseContractInvalid: 'response_contract_invalid'
} as const;
export type ModelSettingsTestDetailsValidationReasonEnum = typeof ModelSettingsTestDetailsValidationReasonEnum[keyof typeof ModelSettingsTestDetailsValidationReasonEnum];

/**
 *
 * @export
 * @interface ModelSettingsTestProblem
 */
export interface ModelSettingsTestProblem {
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestProblem
     */
    error_code: string;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestProblem
     */
    message: string;
    /**
     *
     * @type {boolean}
     * @memberof ModelSettingsTestProblem
     */
    retryable: boolean;
    /**
     *
     * @type {string}
     * @memberof ModelSettingsTestProblem
     */
    workflow_run_id?: string;
    /**
     *
     * @type {ModelSettingsTestDetails}
     * @memberof ModelSettingsTestProblem
     */
    details?: ModelSettingsTestDetails;
}
/**
 * @type ModelSettingsTestResponse
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type ModelSettingsTestResponse = SuccessfulChatConnectionTest | SuccessfulEmbeddingConnectionTest;

/**
 * Approval snapshot for a non-file typed Proposal. Git and Safe Writeback Workflow fields are forbidden.
 * @export
 * @interface NonFileApproval
 */
export interface NonFileApproval {
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    change_hash: string;
    /**
     *
     * @type {NonFileApprovalDecisionEnum}
     * @memberof NonFileApproval
     */
    decision: NonFileApprovalDecisionEnum;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    approved_git_head?: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    decided_at: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    workflow_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof NonFileApproval
     */
    workflow_status_url?: string;
}


/**
 * @export
 */
export const NonFileApprovalDecisionEnum = {
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type NonFileApprovalDecisionEnum = typeof NonFileApprovalDecisionEnum[keyof typeof NonFileApprovalDecisionEnum];

/**
 *
 * @export
 * @interface OllamaChatDraft
 */
export interface OllamaChatDraft {
    /**
     *
     * @type {OllamaChatDraftProviderEnum}
     * @memberof OllamaChatDraft
     */
    provider?: OllamaChatDraftProviderEnum;
    /**
     *
     * @type {OllamaChatDraftApiStyleEnum}
     * @memberof OllamaChatDraft
     */
    api_style?: OllamaChatDraftApiStyleEnum;
    /**
     *
     * @type {OllamaChatDraftBaseUrlEnum}
     * @memberof OllamaChatDraft
     */
    base_url?: OllamaChatDraftBaseUrlEnum;
    /**
     *
     * @type {string}
     * @memberof OllamaChatDraft
     */
    model?: string;
    /**
     *
     * @type {string}
     * @memberof OllamaChatDraft
     */
    model_version?: string;
    /**
     *
     * @type {ModelAPIKeyClear}
     * @memberof OllamaChatDraft
     */
    api_key?: ModelAPIKeyClear;
}


/**
 * @export
 */
export const OllamaChatDraftProviderEnum = {
    Ollama: 'ollama'
} as const;
export type OllamaChatDraftProviderEnum = typeof OllamaChatDraftProviderEnum[keyof typeof OllamaChatDraftProviderEnum];

/**
 * @export
 */
export const OllamaChatDraftApiStyleEnum = {
    ChatCompletions: 'chat_completions'
} as const;
export type OllamaChatDraftApiStyleEnum = typeof OllamaChatDraftApiStyleEnum[keyof typeof OllamaChatDraftApiStyleEnum];

/**
 * @export
 */
export const OllamaChatDraftBaseUrlEnum = {
    Http12700111434: 'http://127.0.0.1:11434'
} as const;
export type OllamaChatDraftBaseUrlEnum = typeof OllamaChatDraftBaseUrlEnum[keyof typeof OllamaChatDraftBaseUrlEnum];

/**
 *
 * @export
 * @interface OllamaChatSettings
 */
export interface OllamaChatSettings {
    /**
     *
     * @type {OllamaChatSettingsProviderEnum}
     * @memberof OllamaChatSettings
     */
    provider?: OllamaChatSettingsProviderEnum;
    /**
     *
     * @type {OllamaChatSettingsApiStyleEnum}
     * @memberof OllamaChatSettings
     */
    api_style?: OllamaChatSettingsApiStyleEnum;
    /**
     *
     * @type {OllamaChatSettingsBaseUrlEnum}
     * @memberof OllamaChatSettings
     */
    base_url?: OllamaChatSettingsBaseUrlEnum;
    /**
     *
     * @type {string}
     * @memberof OllamaChatSettings
     */
    model?: string;
    /**
     *
     * @type {string}
     * @memberof OllamaChatSettings
     */
    model_version?: string;
    /**
     *
     * @type {OllamaChatSettingsApiKeyConfiguredEnum}
     * @memberof OllamaChatSettings
     */
    api_key_configured?: OllamaChatSettingsApiKeyConfiguredEnum;
}


/**
 * @export
 */
export const OllamaChatSettingsProviderEnum = {
    Ollama: 'ollama'
} as const;
export type OllamaChatSettingsProviderEnum = typeof OllamaChatSettingsProviderEnum[keyof typeof OllamaChatSettingsProviderEnum];

/**
 * @export
 */
export const OllamaChatSettingsApiStyleEnum = {
    ChatCompletions: 'chat_completions'
} as const;
export type OllamaChatSettingsApiStyleEnum = typeof OllamaChatSettingsApiStyleEnum[keyof typeof OllamaChatSettingsApiStyleEnum];

/**
 * @export
 */
export const OllamaChatSettingsBaseUrlEnum = {
    Http12700111434: 'http://127.0.0.1:11434'
} as const;
export type OllamaChatSettingsBaseUrlEnum = typeof OllamaChatSettingsBaseUrlEnum[keyof typeof OllamaChatSettingsBaseUrlEnum];

/**
 * @export
 */
export const OllamaChatSettingsApiKeyConfiguredEnum = {
    False: false
} as const;
export type OllamaChatSettingsApiKeyConfiguredEnum = typeof OllamaChatSettingsApiKeyConfiguredEnum[keyof typeof OllamaChatSettingsApiKeyConfiguredEnum];

/**
 *
 * @export
 * @interface OllamaEmbeddingDraft
 */
export interface OllamaEmbeddingDraft {
    /**
     *
     * @type {OllamaEmbeddingDraftProviderEnum}
     * @memberof OllamaEmbeddingDraft
     */
    provider?: OllamaEmbeddingDraftProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OllamaEmbeddingDraft
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OllamaEmbeddingDraft
     */
    model?: string;
    /**
     *
     * @type {number}
     * @memberof OllamaEmbeddingDraft
     */
    dimensions?: number;
}


/**
 * @export
 */
export const OllamaEmbeddingDraftProviderEnum = {
    Ollama: 'ollama'
} as const;
export type OllamaEmbeddingDraftProviderEnum = typeof OllamaEmbeddingDraftProviderEnum[keyof typeof OllamaEmbeddingDraftProviderEnum];

/**
 *
 * @export
 * @interface OllamaEmbeddingSettings
 */
export interface OllamaEmbeddingSettings {
    /**
     *
     * @type {OllamaEmbeddingSettingsProviderEnum}
     * @memberof OllamaEmbeddingSettings
     */
    provider?: OllamaEmbeddingSettingsProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OllamaEmbeddingSettings
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OllamaEmbeddingSettings
     */
    model?: string;
    /**
     *
     * @type {number}
     * @memberof OllamaEmbeddingSettings
     */
    dimensions?: number;
    /**
     *
     * @type {OllamaEmbeddingSettingsApiKeyConfiguredEnum}
     * @memberof OllamaEmbeddingSettings
     */
    api_key_configured?: OllamaEmbeddingSettingsApiKeyConfiguredEnum;
}


/**
 * @export
 */
export const OllamaEmbeddingSettingsProviderEnum = {
    Ollama: 'ollama'
} as const;
export type OllamaEmbeddingSettingsProviderEnum = typeof OllamaEmbeddingSettingsProviderEnum[keyof typeof OllamaEmbeddingSettingsProviderEnum];

/**
 * @export
 */
export const OllamaEmbeddingSettingsApiKeyConfiguredEnum = {
    False: false
} as const;
export type OllamaEmbeddingSettingsApiKeyConfiguredEnum = typeof OllamaEmbeddingSettingsApiKeyConfiguredEnum[keyof typeof OllamaEmbeddingSettingsApiKeyConfiguredEnum];

/**
 *
 * @export
 * @interface OpenAICompatibleChatDraft
 */
export interface OpenAICompatibleChatDraft {
    /**
     *
     * @type {OpenAICompatibleChatDraftProviderEnum}
     * @memberof OpenAICompatibleChatDraft
     */
    provider?: OpenAICompatibleChatDraftProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatDraft
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatDraft
     */
    model?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatDraft
     */
    model_version?: string;
}


/**
 * @export
 */
export const OpenAICompatibleChatDraftProviderEnum = {
    OpenaiCompatible: 'openai-compatible'
} as const;
export type OpenAICompatibleChatDraftProviderEnum = typeof OpenAICompatibleChatDraftProviderEnum[keyof typeof OpenAICompatibleChatDraftProviderEnum];

/**
 *
 * @export
 * @interface OpenAICompatibleChatSettings
 */
export interface OpenAICompatibleChatSettings {
    /**
     *
     * @type {OpenAICompatibleChatSettingsProviderEnum}
     * @memberof OpenAICompatibleChatSettings
     */
    provider?: OpenAICompatibleChatSettingsProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatSettings
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatSettings
     */
    model?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleChatSettings
     */
    model_version?: string;
}


/**
 * @export
 */
export const OpenAICompatibleChatSettingsProviderEnum = {
    OpenaiCompatible: 'openai-compatible'
} as const;
export type OpenAICompatibleChatSettingsProviderEnum = typeof OpenAICompatibleChatSettingsProviderEnum[keyof typeof OpenAICompatibleChatSettingsProviderEnum];

/**
 *
 * @export
 * @interface OpenAICompatibleEmbeddingDraft
 */
export interface OpenAICompatibleEmbeddingDraft {
    /**
     *
     * @type {OpenAICompatibleEmbeddingDraftProviderEnum}
     * @memberof OpenAICompatibleEmbeddingDraft
     */
    provider?: OpenAICompatibleEmbeddingDraftProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleEmbeddingDraft
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleEmbeddingDraft
     */
    model?: string;
    /**
     *
     * @type {number}
     * @memberof OpenAICompatibleEmbeddingDraft
     */
    dimensions?: number;
}


/**
 * @export
 */
export const OpenAICompatibleEmbeddingDraftProviderEnum = {
    OpenaiCompatible: 'openai-compatible'
} as const;
export type OpenAICompatibleEmbeddingDraftProviderEnum = typeof OpenAICompatibleEmbeddingDraftProviderEnum[keyof typeof OpenAICompatibleEmbeddingDraftProviderEnum];

/**
 *
 * @export
 * @interface OpenAICompatibleEmbeddingSettings
 */
export interface OpenAICompatibleEmbeddingSettings {
    /**
     *
     * @type {OpenAICompatibleEmbeddingSettingsProviderEnum}
     * @memberof OpenAICompatibleEmbeddingSettings
     */
    provider?: OpenAICompatibleEmbeddingSettingsProviderEnum;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleEmbeddingSettings
     */
    base_url?: string;
    /**
     *
     * @type {string}
     * @memberof OpenAICompatibleEmbeddingSettings
     */
    model?: string;
    /**
     *
     * @type {number}
     * @memberof OpenAICompatibleEmbeddingSettings
     */
    dimensions?: number;
    /**
     *
     * @type {OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum}
     * @memberof OpenAICompatibleEmbeddingSettings
     */
    api_key_configured?: OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum;
}


/**
 * @export
 */
export const OpenAICompatibleEmbeddingSettingsProviderEnum = {
    OpenaiCompatible: 'openai-compatible'
} as const;
export type OpenAICompatibleEmbeddingSettingsProviderEnum = typeof OpenAICompatibleEmbeddingSettingsProviderEnum[keyof typeof OpenAICompatibleEmbeddingSettingsProviderEnum];

/**
 * @export
 */
export const OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum = {
    True: true
} as const;
export type OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum = typeof OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum[keyof typeof OpenAICompatibleEmbeddingSettingsApiKeyConfiguredEnum];

/**
 *
 * @export
 * @interface OptionalCapabilityStatus
 */
export interface OptionalCapabilityStatus {
    /**
     *
     * @type {OptionalCapabilityStatusStatusEnum}
     * @memberof OptionalCapabilityStatus
     */
    status: OptionalCapabilityStatusStatusEnum;
}


/**
 * @export
 */
export const OptionalCapabilityStatusStatusEnum = {
    Ready: 'ready',
    Unavailable: 'unavailable'
} as const;
export type OptionalCapabilityStatusStatusEnum = typeof OptionalCapabilityStatusStatusEnum[keyof typeof OptionalCapabilityStatusStatusEnum];

/**
 *
 * @export
 * @interface OrganizingAddClaimRequest
 */
export interface OrganizingAddClaimRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingAddClaimRequest
     */
    expected_version: number;
    /**
     *
     * @type {OrganizingAddClaimRequestKindEnum}
     * @memberof OrganizingAddClaimRequest
     */
    kind: OrganizingAddClaimRequestKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingAddClaimRequest
     */
    claim_id: string;
}


/**
 * @export
 */
export const OrganizingAddClaimRequestKindEnum = {
    Claim: 'CLAIM'
} as const;
export type OrganizingAddClaimRequestKindEnum = typeof OrganizingAddClaimRequestKindEnum[keyof typeof OrganizingAddClaimRequestKindEnum];

/**
 *
 * @export
 * @interface OrganizingAddDocumentRevisionRequest
 */
export interface OrganizingAddDocumentRevisionRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingAddDocumentRevisionRequest
     */
    expected_version: number;
    /**
     *
     * @type {OrganizingAddDocumentRevisionRequestKindEnum}
     * @memberof OrganizingAddDocumentRevisionRequest
     */
    kind: OrganizingAddDocumentRevisionRequestKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingAddDocumentRevisionRequest
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingAddDocumentRevisionRequest
     */
    article_revision_id: string;
}


/**
 * @export
 */
export const OrganizingAddDocumentRevisionRequestKindEnum = {
    DocumentRevision: 'DOCUMENT_REVISION'
} as const;
export type OrganizingAddDocumentRevisionRequestKindEnum = typeof OrganizingAddDocumentRevisionRequestKindEnum[keyof typeof OrganizingAddDocumentRevisionRequestKindEnum];

/**
 * @type OrganizingAddMaterialRequest
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type OrganizingAddMaterialRequest = { kind: 'CLAIM' } & OrganizingAddClaimRequest | { kind: 'DOCUMENT_REVISION' } & OrganizingAddDocumentRevisionRequest | { kind: 'SMART_COLLECTION' } & OrganizingAddSmartCollectionRequest | { kind: 'SOURCE_VERSION' } & OrganizingAddSourceVersionRequest;

/**
 *
 * @export
 * @interface OrganizingAddSmartCollectionRequest
 */
export interface OrganizingAddSmartCollectionRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingAddSmartCollectionRequest
     */
    expected_version: number;
    /**
     *
     * @type {OrganizingAddSmartCollectionRequestKindEnum}
     * @memberof OrganizingAddSmartCollectionRequest
     */
    kind: OrganizingAddSmartCollectionRequestKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingAddSmartCollectionRequest
     */
    collection_id: string;
}


/**
 * @export
 */
export const OrganizingAddSmartCollectionRequestKindEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type OrganizingAddSmartCollectionRequestKindEnum = typeof OrganizingAddSmartCollectionRequestKindEnum[keyof typeof OrganizingAddSmartCollectionRequestKindEnum];

/**
 *
 * @export
 * @interface OrganizingAddSourceVersionRequest
 */
export interface OrganizingAddSourceVersionRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingAddSourceVersionRequest
     */
    expected_version: number;
    /**
     *
     * @type {OrganizingAddSourceVersionRequestKindEnum}
     * @memberof OrganizingAddSourceVersionRequest
     */
    kind: OrganizingAddSourceVersionRequestKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingAddSourceVersionRequest
     */
    source_version_id: string;
}


/**
 * @export
 */
export const OrganizingAddSourceVersionRequestKindEnum = {
    SourceVersion: 'SOURCE_VERSION'
} as const;
export type OrganizingAddSourceVersionRequestKindEnum = typeof OrganizingAddSourceVersionRequestKindEnum[keyof typeof OrganizingAddSourceVersionRequestKindEnum];

/**
 * @type OrganizingAvailability
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type OrganizingAvailability = OrganizingAvailabilityOneOf | OrganizingAvailabilityOneOf1;

/**
 *
 * @export
 * @interface OrganizingAvailabilityOneOf
 */
export interface OrganizingAvailabilityOneOf {
    /**
     *
     * @type {OrganizingAvailabilityOneOfAvailableEnum}
     * @memberof OrganizingAvailabilityOneOf
     */
    available?: OrganizingAvailabilityOneOfAvailableEnum;
    /**
     *
     * @type {null}
     * @memberof OrganizingAvailabilityOneOf
     */
    reason?: null;
    /**
     *
     * @type {OrganizingAvailabilityOneOfHrefEnum}
     * @memberof OrganizingAvailabilityOneOf
     */
    href?: OrganizingAvailabilityOneOfHrefEnum;
}


/**
 * @export
 */
export const OrganizingAvailabilityOneOfAvailableEnum = {
    True: true
} as const;
export type OrganizingAvailabilityOneOfAvailableEnum = typeof OrganizingAvailabilityOneOfAvailableEnum[keyof typeof OrganizingAvailabilityOneOfAvailableEnum];

/**
 * @export
 */
export const OrganizingAvailabilityOneOfHrefEnum = {
    AuthoringOrganize: '/authoring/organize'
} as const;
export type OrganizingAvailabilityOneOfHrefEnum = typeof OrganizingAvailabilityOneOfHrefEnum[keyof typeof OrganizingAvailabilityOneOfHrefEnum];

/**
 *
 * @export
 * @interface OrganizingAvailabilityOneOf1
 */
export interface OrganizingAvailabilityOneOf1 {
    /**
     *
     * @type {OrganizingAvailabilityOneOf1AvailableEnum}
     * @memberof OrganizingAvailabilityOneOf1
     */
    available?: OrganizingAvailabilityOneOf1AvailableEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingAvailabilityOneOf1
     */
    reason?: string;
    /**
     *
     * @type {null}
     * @memberof OrganizingAvailabilityOneOf1
     */
    href?: null;
}


/**
 * @export
 */
export const OrganizingAvailabilityOneOf1AvailableEnum = {
    False: false
} as const;
export type OrganizingAvailabilityOneOf1AvailableEnum = typeof OrganizingAvailabilityOneOf1AvailableEnum[keyof typeof OrganizingAvailabilityOneOf1AvailableEnum];

/**
 *
 * @export
 * @interface OrganizingClaimSearchReference
 */
export interface OrganizingClaimSearchReference {
    /**
     *
     * @type {OrganizingClaimSearchReferenceKindEnum}
     * @memberof OrganizingClaimSearchReference
     */
    kind: OrganizingClaimSearchReferenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingClaimSearchReference
     */
    claim_id: string;
}


/**
 * @export
 */
export const OrganizingClaimSearchReferenceKindEnum = {
    Claim: 'CLAIM'
} as const;
export type OrganizingClaimSearchReferenceKindEnum = typeof OrganizingClaimSearchReferenceKindEnum[keyof typeof OrganizingClaimSearchReferenceKindEnum];

/**
 *
 * @export
 * @interface OrganizingConfirmRequest
 */
export interface OrganizingConfirmRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingConfirmRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof OrganizingConfirmRequest
     */
    template_revision_id: string;
}
/**
 *
 * @export
 * @interface OrganizingConfirmResult
 */
export interface OrganizingConfirmResult {
    /**
     *
     * @type {OrganizingDraft}
     * @memberof OrganizingConfirmResult
     */
    draft: OrganizingDraft;
    /**
     *
     * @type {OrganizingSnapshot}
     * @memberof OrganizingConfirmResult
     */
    snapshot: OrganizingSnapshot;
    /**
     *
     * @type {OrganizingConfirmResultDispatchStatusEnum}
     * @memberof OrganizingConfirmResult
     */
    dispatch_status: OrganizingConfirmResultDispatchStatusEnum;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingConfirmResult
     */
    replayed: boolean;
}


/**
 * @export
 */
export const OrganizingConfirmResultDispatchStatusEnum = {
    Pending: 'PENDING'
} as const;
export type OrganizingConfirmResultDispatchStatusEnum = typeof OrganizingConfirmResultDispatchStatusEnum[keyof typeof OrganizingConfirmResultDispatchStatusEnum];

/**
 *
 * @export
 * @interface OrganizingCreateDraftRequest
 */
export interface OrganizingCreateDraftRequest {
    /**
     *
     * @type {string}
     * @memberof OrganizingCreateDraftRequest
     */
    intent: string;
}
/**
 *
 * @export
 * @interface OrganizingDocumentRevisionSearchReference
 */
export interface OrganizingDocumentRevisionSearchReference {
    /**
     *
     * @type {OrganizingDocumentRevisionSearchReferenceKindEnum}
     * @memberof OrganizingDocumentRevisionSearchReference
     */
    kind: OrganizingDocumentRevisionSearchReferenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingDocumentRevisionSearchReference
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingDocumentRevisionSearchReference
     */
    article_revision_id: string;
}


/**
 * @export
 */
export const OrganizingDocumentRevisionSearchReferenceKindEnum = {
    DocumentRevision: 'DOCUMENT_REVISION'
} as const;
export type OrganizingDocumentRevisionSearchReferenceKindEnum = typeof OrganizingDocumentRevisionSearchReferenceKindEnum[keyof typeof OrganizingDocumentRevisionSearchReferenceKindEnum];

/**
 *
 * @export
 * @interface OrganizingDraft
 */
export interface OrganizingDraft {
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    intent: string;
    /**
     *
     * @type {OrganizingDraftStatusEnum}
     * @memberof OrganizingDraft
     */
    status: OrganizingDraftStatusEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    template_revision_id: string | null;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    confirmed_snapshot_id: string | null;
    /**
     *
     * @type {number}
     * @memberof OrganizingDraft
     */
    version: number;
    /**
     *
     * @type {Array<OrganizingMaterial>}
     * @memberof OrganizingDraft
     */
    materials: Array<OrganizingMaterial>;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingDraft
     */
    updated_at: string;
}


/**
 * @export
 */
export const OrganizingDraftStatusEnum = {
    Editing: 'EDITING',
    Confirmed: 'CONFIRMED'
} as const;
export type OrganizingDraftStatusEnum = typeof OrganizingDraftStatusEnum[keyof typeof OrganizingDraftStatusEnum];

/**
 *
 * @export
 * @interface OrganizingDraftCommandResult
 */
export interface OrganizingDraftCommandResult {
    /**
     *
     * @type {OrganizingDraft}
     * @memberof OrganizingDraftCommandResult
     */
    draft: OrganizingDraft;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingDraftCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface OrganizingDraftEnvelope
 */
export interface OrganizingDraftEnvelope {
    /**
     *
     * @type {OrganizingDraft}
     * @memberof OrganizingDraftEnvelope
     */
    draft: OrganizingDraft;
}
/**
 *
 * @export
 * @interface OrganizingEvidence
 */
export interface OrganizingEvidence {
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    chunk_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingEvidence
     */
    excerpt_hash: string;
}
/**
 *
 * @export
 * @interface OrganizingExpectedVersionRequest
 */
export interface OrganizingExpectedVersionRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingExpectedVersionRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface OrganizingMaterial
 */
export interface OrganizingMaterial {
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterial
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterial
     */
    draft_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterial
     */
    workspace_id: string;
    /**
     *
     * @type {OrganizingMaterialKind}
     * @memberof OrganizingMaterial
     */
    kind: OrganizingMaterialKind;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterial
     */
    title: string;
    /**
     *
     * @type {Array<OrganizingMaterialReasonsEnum>}
     * @memberof OrganizingMaterial
     */
    reasons: Array<OrganizingMaterialReasonsEnum>;
    /**
     *
     * @type {OrganizingMaterialOriginEnum}
     * @memberof OrganizingMaterial
     */
    origin: OrganizingMaterialOriginEnum;
    /**
     *
     * @type {OrganizingMaterialAvailabilityEnum}
     * @memberof OrganizingMaterial
     */
    availability: OrganizingMaterialAvailabilityEnum;
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterial
     */
    score: number;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingMaterial
     */
    selected: boolean;
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterial
     */
    position: number;
    /**
     *
     * @type {OrganizingMaterialReference}
     * @memberof OrganizingMaterial
     */
    reference: OrganizingMaterialReference;
    /**
     *
     * @type {Array<OrganizingEvidence>}
     * @memberof OrganizingMaterial
     */
    evidence: Array<OrganizingEvidence>;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterial
     */
    created_at: string;
}


/**
 * @export
 */
export const OrganizingMaterialReasonsEnum = {
    HybridMatch: 'HYBRID_MATCH',
    ProfileMatch: 'PROFILE_MATCH',
    AliasMatch: 'ALIAS_MATCH',
    FormalKnowledge: 'FORMAL_KNOWLEDGE',
    CollectionMember: 'COLLECTION_MEMBER',
    UserAdded: 'USER_ADDED'
} as const;
export type OrganizingMaterialReasonsEnum = typeof OrganizingMaterialReasonsEnum[keyof typeof OrganizingMaterialReasonsEnum];

/**
 * @export
 */
export const OrganizingMaterialOriginEnum = {
    Suggested: 'SUGGESTED',
    User: 'USER'
} as const;
export type OrganizingMaterialOriginEnum = typeof OrganizingMaterialOriginEnum[keyof typeof OrganizingMaterialOriginEnum];

/**
 * @export
 */
export const OrganizingMaterialAvailabilityEnum = {
    Available: 'AVAILABLE',
    Stale: 'STALE',
    Unavailable: 'UNAVAILABLE'
} as const;
export type OrganizingMaterialAvailabilityEnum = typeof OrganizingMaterialAvailabilityEnum[keyof typeof OrganizingMaterialAvailabilityEnum];


/**
 *
 * @export
 */
export const OrganizingMaterialKind = {
    SourceVersion: 'SOURCE_VERSION',
    DocumentRevision: 'DOCUMENT_REVISION',
    Claim: 'CLAIM',
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type OrganizingMaterialKind = typeof OrganizingMaterialKind[keyof typeof OrganizingMaterialKind];

/**
 *
 * @export
 * @interface OrganizingMaterialPolicy
 */
export interface OrganizingMaterialPolicy {
    /**
     *
     * @type {Array<OrganizingMaterialKind>}
     * @memberof OrganizingMaterialPolicy
     */
    allowed_kinds: Array<OrganizingMaterialKind>;
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterialPolicy
     */
    min_materials: number;
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterialPolicy
     */
    max_materials: number;
}
/**
 *
 * @export
 * @interface OrganizingMaterialReference
 */
export interface OrganizingMaterialReference {
    /**
     *
     * @type {OrganizingMaterialKind}
     * @memberof OrganizingMaterialReference
     */
    kind: OrganizingMaterialKind;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    source_version_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    document_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    article_revision_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    claim_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    collection_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    origin_collection_id?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    profile_revision_id?: string;
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterialReference
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    content_hash?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    query_hash?: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialReference
     */
    read_model_revision?: string;
}


/**
 *
 * @export
 * @interface OrganizingMaterialSearchItem
 */
export interface OrganizingMaterialSearchItem {
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialSearchItem
     */
    workspace_id: string;
    /**
     *
     * @type {OrganizingMaterialKind}
     * @memberof OrganizingMaterialSearchItem
     */
    kind: OrganizingMaterialKind;
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialSearchItem
     */
    title: string;
    /**
     *
     * @type {OrganizingMaterialSearchItemAvailabilityEnum}
     * @memberof OrganizingMaterialSearchItem
     */
    availability: OrganizingMaterialSearchItemAvailabilityEnum;
    /**
     *
     * @type {OrganizingMaterialSearchReference}
     * @memberof OrganizingMaterialSearchItem
     */
    reference: OrganizingMaterialSearchReference;
}


/**
 * @export
 */
export const OrganizingMaterialSearchItemAvailabilityEnum = {
    Available: 'AVAILABLE',
    Stale: 'STALE',
    Unavailable: 'UNAVAILABLE'
} as const;
export type OrganizingMaterialSearchItemAvailabilityEnum = typeof OrganizingMaterialSearchItemAvailabilityEnum[keyof typeof OrganizingMaterialSearchItemAvailabilityEnum];

/**
 *
 * @export
 * @interface OrganizingMaterialSearchPage
 */
export interface OrganizingMaterialSearchPage {
    /**
     *
     * @type {string}
     * @memberof OrganizingMaterialSearchPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<OrganizingMaterialSearchItem>}
     * @memberof OrganizingMaterialSearchPage
     */
    items: Array<OrganizingMaterialSearchItem>;
}
/**
 * @type OrganizingMaterialSearchReference
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type OrganizingMaterialSearchReference = { kind: 'CLAIM' } & OrganizingClaimSearchReference | { kind: 'DOCUMENT_REVISION' } & OrganizingDocumentRevisionSearchReference | { kind: 'SMART_COLLECTION' } & OrganizingSmartCollectionSearchReference | { kind: 'SOURCE_VERSION' } & OrganizingSourceVersionSearchReference;

/**
 *
 * @export
 * @interface OrganizingMaterialSelectionRequest
 */
export interface OrganizingMaterialSelectionRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingMaterialSelectionRequest
     */
    expected_version: number;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingMaterialSelectionRequest
     */
    selected: boolean;
}
/**
 *
 * @export
 * @interface OrganizingOutputDefaults
 */
export interface OrganizingOutputDefaults {
    /**
     *
     * @type {string}
     * @memberof OrganizingOutputDefaults
     */
    directory: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingOutputDefaults
     */
    filename_pattern: string;
}
/**
 *
 * @export
 * @interface OrganizingPresentationPolicy
 */
export interface OrganizingPresentationPolicy {
    /**
     *
     * @type {string}
     * @memberof OrganizingPresentationPolicy
     */
    audience: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingPresentationPolicy
     */
    language: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingPresentationPolicy
     */
    tone: string;
    /**
     *
     * @type {OrganizingPresentationPolicyLengthEnum}
     * @memberof OrganizingPresentationPolicy
     */
    length: OrganizingPresentationPolicyLengthEnum;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingPresentationPolicy
     */
    include_code: boolean;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingPresentationPolicy
     */
    include_examples: boolean;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingPresentationPolicy
     */
    include_faq: boolean;
}


/**
 * @export
 */
export const OrganizingPresentationPolicyLengthEnum = {
    Short: 'SHORT',
    Medium: 'MEDIUM',
    Long: 'LONG'
} as const;
export type OrganizingPresentationPolicyLengthEnum = typeof OrganizingPresentationPolicyLengthEnum[keyof typeof OrganizingPresentationPolicyLengthEnum];

/**
 * @type OrganizingRun
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type OrganizingRun = OrganizingRunOneOf | OrganizingRunOneOf1 | OrganizingRunOneOf2;

/**
 *
 * @export
 * @interface OrganizingRunBinding
 */
export interface OrganizingRunBinding {
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    snapshot_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    workflow_run_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    definition_key: string;
    /**
     *
     * @type {number}
     * @memberof OrganizingRunBinding
     */
    definition_version: number;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunBinding
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface OrganizingRunEnvelope
 */
export interface OrganizingRunEnvelope {
    /**
     *
     * @type {OrganizingRun}
     * @memberof OrganizingRunEnvelope
     */
    run: OrganizingRun;
}
/**
 *
 * @export
 * @interface OrganizingRunOneOf
 */
export interface OrganizingRunOneOf {
    /**
     *
     * @type {OrganizingRunOneOfDispatchStatusEnum}
     * @memberof OrganizingRunOneOf
     */
    dispatch_status?: OrganizingRunOneOfDispatchStatusEnum;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf
     */
    workflow_status?: null;
    /**
     *
     * @type {OrganizingRunOneOfRetryableEnum}
     * @memberof OrganizingRunOneOf
     */
    retryable?: OrganizingRunOneOfRetryableEnum;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf
     */
    binding?: null;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf
     */
    result?: null;
}


/**
 * @export
 */
export const OrganizingRunOneOfDispatchStatusEnum = {
    Pending: 'PENDING'
} as const;
export type OrganizingRunOneOfDispatchStatusEnum = typeof OrganizingRunOneOfDispatchStatusEnum[keyof typeof OrganizingRunOneOfDispatchStatusEnum];

/**
 * @export
 */
export const OrganizingRunOneOfRetryableEnum = {
    True: true
} as const;
export type OrganizingRunOneOfRetryableEnum = typeof OrganizingRunOneOfRetryableEnum[keyof typeof OrganizingRunOneOfRetryableEnum];

/**
 *
 * @export
 * @interface OrganizingRunOneOf1
 */
export interface OrganizingRunOneOf1 {
    /**
     *
     * @type {OrganizingRunOneOf1DispatchStatusEnum}
     * @memberof OrganizingRunOneOf1
     */
    dispatch_status?: OrganizingRunOneOf1DispatchStatusEnum;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf1
     */
    workflow_status?: null;
    /**
     *
     * @type {OrganizingRunOneOf1RetryableEnum}
     * @memberof OrganizingRunOneOf1
     */
    retryable?: OrganizingRunOneOf1RetryableEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunOneOf1
     */
    last_error_code?: string;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf1
     */
    binding?: null;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf1
     */
    result?: null;
}


/**
 * @export
 */
export const OrganizingRunOneOf1DispatchStatusEnum = {
    Poisoned: 'POISONED'
} as const;
export type OrganizingRunOneOf1DispatchStatusEnum = typeof OrganizingRunOneOf1DispatchStatusEnum[keyof typeof OrganizingRunOneOf1DispatchStatusEnum];

/**
 * @export
 */
export const OrganizingRunOneOf1RetryableEnum = {
    False: false
} as const;
export type OrganizingRunOneOf1RetryableEnum = typeof OrganizingRunOneOf1RetryableEnum[keyof typeof OrganizingRunOneOf1RetryableEnum];

/**
 * @type OrganizingRunOneOf2
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type OrganizingRunOneOf2 = OrganizingRunOneOf2OneOf | OrganizingRunOneOf2OneOf1;

/**
 *
 * @export
 * @interface OrganizingRunOneOf2OneOf
 */
export interface OrganizingRunOneOf2OneOf {
    /**
     *
     * @type {OrganizingRunOneOf2OneOfWorkflowStatusEnum}
     * @memberof OrganizingRunOneOf2OneOf
     */
    workflow_status?: OrganizingRunOneOf2OneOfWorkflowStatusEnum;
    /**
     *
     * @type {OrganizingRunResult}
     * @memberof OrganizingRunOneOf2OneOf
     */
    result?: OrganizingRunResult;
}


/**
 * @export
 */
export const OrganizingRunOneOf2OneOfWorkflowStatusEnum = {
    Succeeded: 'succeeded'
} as const;
export type OrganizingRunOneOf2OneOfWorkflowStatusEnum = typeof OrganizingRunOneOf2OneOfWorkflowStatusEnum[keyof typeof OrganizingRunOneOf2OneOfWorkflowStatusEnum];

/**
 *
 * @export
 * @interface OrganizingRunOneOf2OneOf1
 */
export interface OrganizingRunOneOf2OneOf1 {
    /**
     *
     * @type {OrganizingRunOneOf2OneOf1WorkflowStatusEnum}
     * @memberof OrganizingRunOneOf2OneOf1
     */
    workflow_status?: OrganizingRunOneOf2OneOf1WorkflowStatusEnum;
    /**
     *
     * @type {null}
     * @memberof OrganizingRunOneOf2OneOf1
     */
    result?: null;
}


/**
 * @export
 */
export const OrganizingRunOneOf2OneOf1WorkflowStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type OrganizingRunOneOf2OneOf1WorkflowStatusEnum = typeof OrganizingRunOneOf2OneOf1WorkflowStatusEnum[keyof typeof OrganizingRunOneOf2OneOf1WorkflowStatusEnum];

/**
 *
 * @export
 * @interface OrganizingRunResult
 */
export interface OrganizingRunResult {
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    run_binding_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    snapshot_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    workflow_run_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    node_run_id: string;
    /**
     *
     * @type {OrganizingRunResultKindEnum}
     * @memberof OrganizingRunResult
     */
    kind: OrganizingRunResultKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    result_ref: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    result_hash: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingRunResult
     */
    created_at: string;
}


/**
 * @export
 */
export const OrganizingRunResultKindEnum = {
    Artifact: 'ARTIFACT',
    MergeProposal: 'MERGE_PROPOSAL'
} as const;
export type OrganizingRunResultKindEnum = typeof OrganizingRunResultKindEnum[keyof typeof OrganizingRunResultKindEnum];

/**
 *
 * @export
 * @interface OrganizingSmartCollectionSearchReference
 */
export interface OrganizingSmartCollectionSearchReference {
    /**
     *
     * @type {OrganizingSmartCollectionSearchReferenceKindEnum}
     * @memberof OrganizingSmartCollectionSearchReference
     */
    kind: OrganizingSmartCollectionSearchReferenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingSmartCollectionSearchReference
     */
    collection_id: string;
}


/**
 * @export
 */
export const OrganizingSmartCollectionSearchReferenceKindEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type OrganizingSmartCollectionSearchReferenceKindEnum = typeof OrganizingSmartCollectionSearchReferenceKindEnum[keyof typeof OrganizingSmartCollectionSearchReferenceKindEnum];

/**
 *
 * @export
 * @interface OrganizingSnapshot
 */
export interface OrganizingSnapshot {
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    draft_id: string;
    /**
     *
     * @type {number}
     * @memberof OrganizingSnapshot
     */
    draft_version: number;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    template_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    template_revision_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    template_hash: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    intent: string;
    /**
     *
     * @type {Array<OrganizingSnapshotMaterial>}
     * @memberof OrganizingSnapshot
     */
    materials: Array<OrganizingSnapshotMaterial>;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    canonical_hash: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingSnapshot
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface OrganizingSnapshotEnvelope
 */
export interface OrganizingSnapshotEnvelope {
    /**
     *
     * @type {OrganizingSnapshot}
     * @memberof OrganizingSnapshotEnvelope
     */
    snapshot: OrganizingSnapshot;
}
/**
 *
 * @export
 * @interface OrganizingSnapshotMaterial
 */
export interface OrganizingSnapshotMaterial {
    /**
     *
     * @type {number}
     * @memberof OrganizingSnapshotMaterial
     */
    position: number;
    /**
     *
     * @type {OrganizingMaterialReference}
     * @memberof OrganizingSnapshotMaterial
     */
    reference: OrganizingMaterialReference;
    /**
     *
     * @type {Array<OrganizingEvidence>}
     * @memberof OrganizingSnapshotMaterial
     */
    evidence: Array<OrganizingEvidence>;
}
/**
 *
 * @export
 * @interface OrganizingSourceVersionSearchReference
 */
export interface OrganizingSourceVersionSearchReference {
    /**
     *
     * @type {OrganizingSourceVersionSearchReferenceKindEnum}
     * @memberof OrganizingSourceVersionSearchReference
     */
    kind: OrganizingSourceVersionSearchReferenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingSourceVersionSearchReference
     */
    source_version_id: string;
}


/**
 * @export
 */
export const OrganizingSourceVersionSearchReferenceKindEnum = {
    SourceVersion: 'SOURCE_VERSION'
} as const;
export type OrganizingSourceVersionSearchReferenceKindEnum = typeof OrganizingSourceVersionSearchReferenceKindEnum[keyof typeof OrganizingSourceVersionSearchReferenceKindEnum];

/**
 *
 * @export
 * @interface OrganizingSuggestRequest
 */
export interface OrganizingSuggestRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingSuggestRequest
     */
    expected_version: number;
    /**
     *
     * @type {number}
     * @memberof OrganizingSuggestRequest
     */
    limit?: number;
}
/**
 *
 * @export
 * @interface OrganizingTemplate
 */
export interface OrganizingTemplate {
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    workspace_id: string | null;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    description: string;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingTemplate
     */
    built_in: boolean;
    /**
     *
     * @type {OrganizingTemplateKind}
     * @memberof OrganizingTemplate
     */
    kind: OrganizingTemplateKind;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    current_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof OrganizingTemplate
     */
    version: number;
    /**
     *
     * @type {OrganizingTemplateRevision}
     * @memberof OrganizingTemplate
     */
    current_revision: OrganizingTemplateRevision;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplate
     */
    updated_at: string;
}


/**
 *
 * @export
 * @interface OrganizingTemplateCloneRequest
 */
export interface OrganizingTemplateCloneRequest {
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateCloneRequest
     */
    name: string;
}
/**
 *
 * @export
 * @interface OrganizingTemplateCommandResult
 */
export interface OrganizingTemplateCommandResult {
    /**
     *
     * @type {OrganizingTemplate}
     * @memberof OrganizingTemplateCommandResult
     */
    template: OrganizingTemplate;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingTemplateCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface OrganizingTemplateCreateRequest
 */
export interface OrganizingTemplateCreateRequest {
    /**
     *
     * @type {OrganizingTemplateDeclaration}
     * @memberof OrganizingTemplateCreateRequest
     */
    declaration: OrganizingTemplateDeclaration;
}
/**
 * Constrained declaration only. Prompt text, tool lists, permissions, Workflow nodes, and model credentials are not accepted.
 * @export
 * @interface OrganizingTemplateDeclaration
 */
export interface OrganizingTemplateDeclaration {
    /**
     *
     * @type {OrganizingTemplateDeclarationSchemaVersionEnum}
     * @memberof OrganizingTemplateDeclaration
     */
    schema_version: OrganizingTemplateDeclarationSchemaVersionEnum;
    /**
     *
     * @type {OrganizingTemplateKind}
     * @memberof OrganizingTemplateDeclaration
     */
    kind: OrganizingTemplateKind;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateDeclaration
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateDeclaration
     */
    description: string;
    /**
     *
     * @type {OrganizingMaterialPolicy}
     * @memberof OrganizingTemplateDeclaration
     */
    materials: OrganizingMaterialPolicy;
    /**
     * Must contain exactly one required section for each governance key: conflicts, gaps, and sources.
     * @type {Array<OrganizingTemplateSection>}
     * @memberof OrganizingTemplateDeclaration
     */
    sections: Array<OrganizingTemplateSection>;
    /**
     *
     * @type {OrganizingPresentationPolicy}
     * @memberof OrganizingTemplateDeclaration
     */
    presentation: OrganizingPresentationPolicy;
    /**
     *
     * @type {OrganizingOutputDefaults}
     * @memberof OrganizingTemplateDeclaration
     */
    output: OrganizingOutputDefaults;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateDeclaration
     */
    additional_instructions: string;
}


/**
 * @export
 */
export const OrganizingTemplateDeclarationSchemaVersionEnum = {
    OrganizingTemplateV1: 'organizing-template/v1'
} as const;
export type OrganizingTemplateDeclarationSchemaVersionEnum = typeof OrganizingTemplateDeclarationSchemaVersionEnum[keyof typeof OrganizingTemplateDeclarationSchemaVersionEnum];

/**
 *
 * @export
 * @interface OrganizingTemplateEnvelope
 */
export interface OrganizingTemplateEnvelope {
    /**
     *
     * @type {OrganizingTemplate}
     * @memberof OrganizingTemplateEnvelope
     */
    template: OrganizingTemplate;
}

/**
 *
 * @export
 */
export const OrganizingTemplateKind = {
    TopicArticle: 'TOPIC_ARTICLE',
    MergeDocuments: 'MERGE_DOCUMENTS',
    KnowledgeReport: 'KNOWLEDGE_REPORT',
    InterviewReview: 'INTERVIEW_REVIEW'
} as const;
export type OrganizingTemplateKind = typeof OrganizingTemplateKind[keyof typeof OrganizingTemplateKind];

/**
 *
 * @export
 * @interface OrganizingTemplatePage
 */
export interface OrganizingTemplatePage {
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplatePage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<OrganizingTemplate>}
     * @memberof OrganizingTemplatePage
     */
    items: Array<OrganizingTemplate>;
}
/**
 *
 * @export
 * @interface OrganizingTemplateReviseRequest
 */
export interface OrganizingTemplateReviseRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingTemplateReviseRequest
     */
    expected_version: number;
    /**
     *
     * @type {OrganizingTemplateDeclaration}
     * @memberof OrganizingTemplateReviseRequest
     */
    declaration: OrganizingTemplateDeclaration;
}
/**
 *
 * @export
 * @interface OrganizingTemplateRevision
 */
export interface OrganizingTemplateRevision {
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateRevision
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateRevision
     */
    template_id: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateRevision
     */
    workspace_id: string | null;
    /**
     *
     * @type {number}
     * @memberof OrganizingTemplateRevision
     */
    revision_no: number;
    /**
     *
     * @type {OrganizingTemplateKind}
     * @memberof OrganizingTemplateRevision
     */
    kind: OrganizingTemplateKind;
    /**
     *
     * @type {OrganizingTemplateRevisionSchemaVersionEnum}
     * @memberof OrganizingTemplateRevision
     */
    schema_version: OrganizingTemplateRevisionSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateRevision
     */
    canonical_hash: string;
    /**
     *
     * @type {OrganizingTemplateDeclaration}
     * @memberof OrganizingTemplateRevision
     */
    declaration: OrganizingTemplateDeclaration;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const OrganizingTemplateRevisionSchemaVersionEnum = {
    OrganizingTemplateV1: 'organizing-template/v1'
} as const;
export type OrganizingTemplateRevisionSchemaVersionEnum = typeof OrganizingTemplateRevisionSchemaVersionEnum[keyof typeof OrganizingTemplateRevisionSchemaVersionEnum];

/**
 *
 * @export
 * @interface OrganizingTemplateSection
 */
export interface OrganizingTemplateSection {
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateSection
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingTemplateSection
     */
    title: string;
    /**
     *
     * @type {boolean}
     * @memberof OrganizingTemplateSection
     */
    required: boolean;
}
/**
 *
 * @export
 * @interface OrganizingUpdateDraftRequest
 */
export interface OrganizingUpdateDraftRequest {
    /**
     *
     * @type {number}
     * @memberof OrganizingUpdateDraftRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof OrganizingUpdateDraftRequest
     */
    intent: string;
    /**
     *
     * @type {string}
     * @memberof OrganizingUpdateDraftRequest
     */
    template_revision_id: string;
}
/**
 *
 * @export
 * @interface PendingWorkflowHumanTask
 */
export interface PendingWorkflowHumanTask {
    /**
     *
     * @type {string}
     * @memberof PendingWorkflowHumanTask
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof PendingWorkflowHumanTask
     */
    run_id: string;
    /**
     *
     * @type {string}
     * @memberof PendingWorkflowHumanTask
     */
    node_run_id: string;
    /**
     *
     * @type {PendingWorkflowHumanTaskStatusEnum}
     * @memberof PendingWorkflowHumanTask
     */
    status: PendingWorkflowHumanTaskStatusEnum;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof PendingWorkflowHumanTask
     */
    expected_input_schema: { [key: string]: JSONValue; };
    /**
     *
     * @type {number}
     * @memberof PendingWorkflowHumanTask
     */
    target_version: number;
    /**
     *
     * @type {string}
     * @memberof PendingWorkflowHumanTask
     */
    expires_at: string | null;
    /**
     *
     * @type {string}
     * @memberof PendingWorkflowHumanTask
     */
    created_at: string;
    /**
     *
     * @type {WorkflowHumanTaskReview}
     * @memberof PendingWorkflowHumanTask
     */
    review: WorkflowHumanTaskReview | null;
}


/**
 * @export
 */
export const PendingWorkflowHumanTaskStatusEnum = {
    Pending: 'pending'
} as const;
export type PendingWorkflowHumanTaskStatusEnum = typeof PendingWorkflowHumanTaskStatusEnum[keyof typeof PendingWorkflowHumanTaskStatusEnum];

/**
 *
 * @export
 * @interface Problem
 */
export interface Problem {
    /**
     *
     * @type {string}
     * @memberof Problem
     */
    error_code: string;
    /**
     *
     * @type {string}
     * @memberof Problem
     */
    message: string;
    /**
     *
     * @type {boolean}
     * @memberof Problem
     */
    retryable: boolean;
    /**
     *
     * @type {string}
     * @memberof Problem
     */
    workflow_run_id?: string;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof Problem
     */
    details?: { [key: string]: JSONValue; };
}
/**
 * @type Proposal
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type Proposal = { proposal_type: 'downstream_update' } & DownstreamUpdateProposal | { proposal_type: 'file_patch' } & FilePatchProposal | { proposal_type: 'knowledge_change' } & KnowledgeChangeProposal | { proposal_type: 'publish_artifact' } & PublishArtifactProposal | { proposal_type: 'restore_document' } & RestoreDocumentProposal;

/**
 *
 * @export
 * @interface ProposalCurrentContent
 */
export interface ProposalCurrentContent {
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    target_path: string;
    /**
     *
     * @type {ProposalCurrentContentTargetModeEnum}
     * @memberof ProposalCurrentContent
     */
    target_mode: ProposalCurrentContentTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    current_hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalCurrentContent
     */
    base_hash: string;
    /**
     * True exactly when current_hash equals base_hash.
     * @type {boolean}
     * @memberof ProposalCurrentContent
     */
    base_hash_match: boolean;
}


/**
 * @export
 */
export const ProposalCurrentContentTargetModeEnum = {
    Replace: 'REPLACE',
    CreateOnly: 'CREATE_ONLY'
} as const;
export type ProposalCurrentContentTargetModeEnum = typeof ProposalCurrentContentTargetModeEnum[keyof typeof ProposalCurrentContentTargetModeEnum];

/**
 *
 * @export
 * @interface ProposalDecisionRequest
 */
export interface ProposalDecisionRequest {
    /**
     *
     * @type {string}
     * @memberof ProposalDecisionRequest
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalDecisionRequest
     */
    change_hash: string;
    /**
     *
     * @type {ProposalDecisionRequestDecisionEnum}
     * @memberof ProposalDecisionRequest
     */
    decision: ProposalDecisionRequestDecisionEnum;
}


/**
 * @export
 */
export const ProposalDecisionRequestDecisionEnum = {
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type ProposalDecisionRequestDecisionEnum = typeof ProposalDecisionRequestDecisionEnum[keyof typeof ProposalDecisionRequestDecisionEnum];

/**
 *
 * @export
 * @interface ProposalPage
 */
export interface ProposalPage {
    /**
     *
     * @type {Array<ProposalSummary>}
     * @memberof ProposalPage
     */
    items: Array<ProposalSummary>;
    /**
     *
     * @type {string}
     * @memberof ProposalPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface ProposalRevision
 */
export interface ProposalRevision {
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevision
     */
    revision_no: number;
    /**
     *
     * @type {ProposalRevisionTargetModeEnum}
     * @memberof ProposalRevision
     */
    target_mode: ProposalRevisionTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    base_hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    evidence_summary: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevision
     */
    created_at: string;
}


/**
 * @export
 */
export const ProposalRevisionTargetModeEnum = {
    Replace: 'REPLACE',
    CreateOnly: 'CREATE_ONLY'
} as const;
export type ProposalRevisionTargetModeEnum = typeof ProposalRevisionTargetModeEnum[keyof typeof ProposalRevisionTargetModeEnum];

/**
 *
 * @export
 * @interface ProposalRevisionAppendResponse
 */
export interface ProposalRevisionAppendResponse {
    /**
     *
     * @type {ProposalRevisionAppendResponseProposalTypeEnum}
     * @memberof ProposalRevisionAppendResponse
     */
    proposal_type: ProposalRevisionAppendResponseProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionAppendResponse
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionAppendResponse
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionAppendResponse
     */
    target_path: string;
    /**
     *
     * @type {ProposalRevisionAppendResponseStatusEnum}
     * @memberof ProposalRevisionAppendResponse
     */
    status: ProposalRevisionAppendResponseStatusEnum;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof ProposalRevisionAppendResponse
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionAppendResponse
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof ProposalRevisionAppendResponse
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {ProposalRevision}
     * @memberof ProposalRevisionAppendResponse
     */
    revision: ProposalRevision;
    /**
     *
     * @type {null}
     * @memberof ProposalRevisionAppendResponse
     */
    approval: null;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionAppendResponse
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionAppendResponse
     */
    updated_at: string;
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionAppendResponse
     */
    replayed: boolean;
}


/**
 * @export
 */
export const ProposalRevisionAppendResponseProposalTypeEnum = {
    FilePatch: 'file_patch'
} as const;
export type ProposalRevisionAppendResponseProposalTypeEnum = typeof ProposalRevisionAppendResponseProposalTypeEnum[keyof typeof ProposalRevisionAppendResponseProposalTypeEnum];

/**
 * @export
 */
export const ProposalRevisionAppendResponseStatusEnum = {
    ReadyForReview: 'ready_for_review'
} as const;
export type ProposalRevisionAppendResponseStatusEnum = typeof ProposalRevisionAppendResponseStatusEnum[keyof typeof ProposalRevisionAppendResponseStatusEnum];

/**
 *
 * @export
 * @interface ProposalRevisionBaseSnapshot
 */
export interface ProposalRevisionBaseSnapshot {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionBaseSnapshot
     */
    hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionBaseSnapshot
     */
    content: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionBaseSnapshot
     */
    byte_size: number;
    /**
     *
     * @type {ProposalRevisionBaseSnapshotSchemaVersionEnum}
     * @memberof ProposalRevisionBaseSnapshot
     */
    schema_version: ProposalRevisionBaseSnapshotSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionBaseSnapshot
     */
    created_at: string;
}


/**
 * @export
 */
export const ProposalRevisionBaseSnapshotSchemaVersionEnum = {
    ProposalBaseSnapshotV1: 'proposal-base-snapshot/v1'
} as const;
export type ProposalRevisionBaseSnapshotSchemaVersionEnum = typeof ProposalRevisionBaseSnapshotSchemaVersionEnum[keyof typeof ProposalRevisionBaseSnapshotSchemaVersionEnum];

/**
 *
 * @export
 * @interface ProposalRevisionCapability
 */
export interface ProposalRevisionCapability {
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionCapability
     */
    editable: boolean;
    /**
     *
     * @type {ProposalRevisionCapabilityReasonEnum}
     * @memberof ProposalRevisionCapability
     */
    reason: ProposalRevisionCapabilityReasonEnum;
}


/**
 * @export
 */
export const ProposalRevisionCapabilityReasonEnum = {
    Available: 'AVAILABLE',
    ProposalRevisionUnsupportedType: 'PROPOSAL_REVISION_UNSUPPORTED_TYPE',
    ProposalRevisionUnsupportedMode: 'PROPOSAL_REVISION_UNSUPPORTED_MODE',
    ProposalRevisionStatusNotEditable: 'PROPOSAL_REVISION_STATUS_NOT_EDITABLE',
    ProposalRevisionStale: 'PROPOSAL_REVISION_STALE',
    ProposalRevisionWorkflowActive: 'PROPOSAL_REVISION_WORKFLOW_ACTIVE',
    ProposalRevisionSideEffectStarted: 'PROPOSAL_REVISION_SIDE_EFFECT_STARTED',
    ProposalRevisionInputTooLarge: 'PROPOSAL_REVISION_INPUT_TOO_LARGE',
    ProposalMergeEngineUnavailable: 'PROPOSAL_MERGE_ENGINE_UNAVAILABLE'
} as const;
export type ProposalRevisionCapabilityReasonEnum = typeof ProposalRevisionCapabilityReasonEnum[keyof typeof ProposalRevisionCapabilityReasonEnum];

/**
 *
 * @export
 * @interface ProposalRevisionHistoryApproval
 */
export interface ProposalRevisionHistoryApproval {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    change_hash: string;
    /**
     *
     * @type {ProposalRevisionHistoryApprovalDecisionEnum}
     * @memberof ProposalRevisionHistoryApproval
     */
    decision: ProposalRevisionHistoryApprovalDecisionEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    decided_at: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryApproval
     */
    approved_git_head?: string;
}


/**
 * @export
 */
export const ProposalRevisionHistoryApprovalDecisionEnum = {
    Approved: 'approved',
    Rejected: 'rejected'
} as const;
export type ProposalRevisionHistoryApprovalDecisionEnum = typeof ProposalRevisionHistoryApprovalDecisionEnum[keyof typeof ProposalRevisionHistoryApprovalDecisionEnum];

/**
 *
 * @export
 * @interface ProposalRevisionHistoryDetail
 */
export interface ProposalRevisionHistoryDetail {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryDetail
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryDetail
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryDetail
     */
    current_revision_id: string;
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionHistoryDetail
     */
    current: boolean;
    /**
     *
     * @type {HistoricalProposalRevision}
     * @memberof ProposalRevisionHistoryDetail
     */
    revision: HistoricalProposalRevision;
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionHistoryDetail
     */
    base_available: boolean;
    /**
     *
     * @type {ProposalRevisionBaseSnapshot}
     * @memberof ProposalRevisionHistoryDetail
     */
    base_snapshot: ProposalRevisionBaseSnapshot | null;
    /**
     *
     * @type {ProposalRevisionLineage}
     * @memberof ProposalRevisionHistoryDetail
     */
    lineage: ProposalRevisionLineage | null;
    /**
     *
     * @type {ProposalRevisionHistoryApproval}
     * @memberof ProposalRevisionHistoryDetail
     */
    approval: ProposalRevisionHistoryApproval | null;
    /**
     *
     * @type {ProposalRevisionWorkflow}
     * @memberof ProposalRevisionHistoryDetail
     */
    workflow: ProposalRevisionWorkflow | null;
}
/**
 *
 * @export
 * @interface ProposalRevisionHistoryItem
 */
export interface ProposalRevisionHistoryItem {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionHistoryItem
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    target_path: string;
    /**
     *
     * @type {ProposalRevisionHistoryItemTargetModeEnum}
     * @memberof ProposalRevisionHistoryItem
     */
    target_mode: ProposalRevisionHistoryItemTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    base_hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    change_hash: string;
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionHistoryItem
     */
    base_available: boolean;
    /**
     *
     * @type {boolean}
     * @memberof ProposalRevisionHistoryItem
     */
    current: boolean;
    /**
     *
     * @type {ProposalRevisionHistoryApproval}
     * @memberof ProposalRevisionHistoryItem
     */
    approval: ProposalRevisionHistoryApproval | null;
    /**
     *
     * @type {ProposalRevisionWorkflow}
     * @memberof ProposalRevisionHistoryItem
     */
    workflow: ProposalRevisionWorkflow | null;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionHistoryItem
     */
    created_at: string;
}


/**
 * @export
 */
export const ProposalRevisionHistoryItemTargetModeEnum = {
    Replace: 'REPLACE'
} as const;
export type ProposalRevisionHistoryItemTargetModeEnum = typeof ProposalRevisionHistoryItemTargetModeEnum[keyof typeof ProposalRevisionHistoryItemTargetModeEnum];

/**
 *
 * @export
 * @interface ProposalRevisionHistoryPage
 */
export interface ProposalRevisionHistoryPage {
    /**
     *
     * @type {Array<ProposalRevisionHistoryItem>}
     * @memberof ProposalRevisionHistoryPage
     */
    items: Array<ProposalRevisionHistoryItem>;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionHistoryPage
     */
    next_before_revision_no?: number;
}
/**
 *
 * @export
 * @interface ProposalRevisionLineage
 */
export interface ProposalRevisionLineage {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionLineage
     */
    source_revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionLineage
     */
    source_change_hash: string;
    /**
     *
     * @type {ProposalRevisionLineageKindEnum}
     * @memberof ProposalRevisionLineage
     */
    kind: ProposalRevisionLineageKindEnum;
    /**
     *
     * @type {ProposalRevisionLineageMergeAlgorithmEnum}
     * @memberof ProposalRevisionLineage
     */
    merge_algorithm: ProposalRevisionLineageMergeAlgorithmEnum;
    /**
     *
     * @type {ProposalRevisionLineageMergeAlgorithmVersionEnum}
     * @memberof ProposalRevisionLineage
     */
    merge_algorithm_version: ProposalRevisionLineageMergeAlgorithmVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionLineage
     */
    merge_fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionLineage
     */
    created_at: string;
}


/**
 * @export
 */
export const ProposalRevisionLineageKindEnum = {
    DirectEdit: 'DIRECT_EDIT',
    ThreeWayMerge: 'THREE_WAY_MERGE'
} as const;
export type ProposalRevisionLineageKindEnum = typeof ProposalRevisionLineageKindEnum[keyof typeof ProposalRevisionLineageKindEnum];

/**
 * @export
 */
export const ProposalRevisionLineageMergeAlgorithmEnum = {
    GitMergeFile: 'git-merge-file'
} as const;
export type ProposalRevisionLineageMergeAlgorithmEnum = typeof ProposalRevisionLineageMergeAlgorithmEnum[keyof typeof ProposalRevisionLineageMergeAlgorithmEnum];

/**
 * @export
 */
export const ProposalRevisionLineageMergeAlgorithmVersionEnum = {
    Diff3MyersMarker32V1: 'diff3/myers/marker32/v1'
} as const;
export type ProposalRevisionLineageMergeAlgorithmVersionEnum = typeof ProposalRevisionLineageMergeAlgorithmVersionEnum[keyof typeof ProposalRevisionLineageMergeAlgorithmVersionEnum];

/**
 *
 * @export
 * @interface ProposalRevisionMergeConflict
 */
export interface ProposalRevisionMergeConflict {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergeConflict
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionMergeConflict
     */
    ordinal: number;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergeConflict
     */
    base: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergeConflict
     */
    current: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergeConflict
     */
    proposed: string;
}
/**
 *
 * @export
 * @interface ProposalRevisionMergePreview
 */
export interface ProposalRevisionMergePreview {
    /**
     *
     * @type {ProposalRevisionMergePreviewSchemaVersionEnum}
     * @memberof ProposalRevisionMergePreview
     */
    schema_version: ProposalRevisionMergePreviewSchemaVersionEnum;
    /**
     *
     * @type {ProposalRevisionMergePreviewMergeAlgorithmEnum}
     * @memberof ProposalRevisionMergePreview
     */
    merge_algorithm: ProposalRevisionMergePreviewMergeAlgorithmEnum;
    /**
     *
     * @type {ProposalRevisionMergePreviewMergeAlgorithmVersionEnum}
     * @memberof ProposalRevisionMergePreview
     */
    merge_algorithm_version: ProposalRevisionMergePreviewMergeAlgorithmVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    merge_fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    proposal_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionMergePreview
     */
    proposal_version: number;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    source_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionMergePreview
     */
    source_revision_no: number;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    source_change_hash: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreview
     */
    target_path: string;
    /**
     *
     * @type {ProposalRevisionMergePreviewTargetModeEnum}
     * @memberof ProposalRevisionMergePreview
     */
    target_mode: ProposalRevisionMergePreviewTargetModeEnum;
    /**
     *
     * @type {ProposalRevisionTextSnapshot}
     * @memberof ProposalRevisionMergePreview
     */
    base: ProposalRevisionTextSnapshot;
    /**
     *
     * @type {ProposalRevisionTextSnapshot}
     * @memberof ProposalRevisionMergePreview
     */
    current: ProposalRevisionTextSnapshot;
    /**
     *
     * @type {ProposalRevisionTextSnapshot}
     * @memberof ProposalRevisionMergePreview
     */
    proposed: ProposalRevisionTextSnapshot;
    /**
     *
     * @type {ProposalRevisionTextSnapshot}
     * @memberof ProposalRevisionMergePreview
     */
    candidate: ProposalRevisionTextSnapshot;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionMergePreview
     */
    conflict_count: number;
    /**
     *
     * @type {Array<ProposalRevisionMergeConflict>}
     * @memberof ProposalRevisionMergePreview
     */
    conflicts: Array<ProposalRevisionMergeConflict>;
}


/**
 * @export
 */
export const ProposalRevisionMergePreviewSchemaVersionEnum = {
    ProposalTextMergePreviewV1: 'proposal-text-merge-preview/v1'
} as const;
export type ProposalRevisionMergePreviewSchemaVersionEnum = typeof ProposalRevisionMergePreviewSchemaVersionEnum[keyof typeof ProposalRevisionMergePreviewSchemaVersionEnum];

/**
 * @export
 */
export const ProposalRevisionMergePreviewMergeAlgorithmEnum = {
    GitMergeFile: 'git-merge-file'
} as const;
export type ProposalRevisionMergePreviewMergeAlgorithmEnum = typeof ProposalRevisionMergePreviewMergeAlgorithmEnum[keyof typeof ProposalRevisionMergePreviewMergeAlgorithmEnum];

/**
 * @export
 */
export const ProposalRevisionMergePreviewMergeAlgorithmVersionEnum = {
    Diff3MyersMarker32V1: 'diff3/myers/marker32/v1'
} as const;
export type ProposalRevisionMergePreviewMergeAlgorithmVersionEnum = typeof ProposalRevisionMergePreviewMergeAlgorithmVersionEnum[keyof typeof ProposalRevisionMergePreviewMergeAlgorithmVersionEnum];

/**
 * @export
 */
export const ProposalRevisionMergePreviewTargetModeEnum = {
    Replace: 'REPLACE'
} as const;
export type ProposalRevisionMergePreviewTargetModeEnum = typeof ProposalRevisionMergePreviewTargetModeEnum[keyof typeof ProposalRevisionMergePreviewTargetModeEnum];

/**
 *
 * @export
 * @interface ProposalRevisionMergePreviewRequest
 */
export interface ProposalRevisionMergePreviewRequest {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreviewRequest
     */
    source_revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionMergePreviewRequest
     */
    source_change_hash: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionMergePreviewRequest
     */
    expected_proposal_version: number;
}
/**
 *
 * @export
 * @interface ProposalRevisionTextSnapshot
 */
export interface ProposalRevisionTextSnapshot {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionTextSnapshot
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionTextSnapshot
     */
    hash: string;
    /**
     *
     * @type {number}
     * @memberof ProposalRevisionTextSnapshot
     */
    byte_size: number;
}
/**
 *
 * @export
 * @interface ProposalRevisionWorkflow
 */
export interface ProposalRevisionWorkflow {
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionWorkflow
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalRevisionWorkflow
     */
    status_url: string;
}

/**
 * Explicit immutable Proposal-level approval and list-filter fact. Revision risk remains an independent free-form explanation and never derives this level.
 * @export
 */
export const ProposalRiskLevel = {
    Critical: 'CRITICAL',
    High: 'HIGH',
    Medium: 'MEDIUM',
    Low: 'LOW'
} as const;
export type ProposalRiskLevel = typeof ProposalRiskLevel[keyof typeof ProposalRiskLevel];

/**
 *
 * @export
 * @interface ProposalSummary
 */
export interface ProposalSummary {
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    workspace_id: string;
    /**
     *
     * @type {ProposalSummaryProposalTypeEnum}
     * @memberof ProposalSummary
     */
    proposal_type: ProposalSummaryProposalTypeEnum;
    /**
     *
     * @type {ProposalSummaryStatusEnum}
     * @memberof ProposalSummary
     */
    status: ProposalSummaryStatusEnum;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    target: string;
    /**
     *
     * @type {ProposalRiskLevel}
     * @memberof ProposalSummary
     */
    risk_level: ProposalRiskLevel;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    revision_id: string;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    change_hash: string;
    /**
     *
     * @type {number}
     * @memberof ProposalSummary
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof ProposalSummary
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {Approval}
     * @memberof ProposalSummary
     */
    approval?: Approval;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ProposalSummary
     */
    updated_at: string;
}


/**
 * @export
 */
export const ProposalSummaryProposalTypeEnum = {
    FilePatch: 'file_patch',
    RestoreDocument: 'restore_document',
    KnowledgeChange: 'knowledge_change',
    PublishArtifact: 'publish_artifact',
    DownstreamUpdate: 'downstream_update'
} as const;
export type ProposalSummaryProposalTypeEnum = typeof ProposalSummaryProposalTypeEnum[keyof typeof ProposalSummaryProposalTypeEnum];

/**
 * @export
 */
export const ProposalSummaryStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type ProposalSummaryStatusEnum = typeof ProposalSummaryStatusEnum[keyof typeof ProposalSummaryStatusEnum];

/**
 * @type PublicationBinding
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type PublicationBinding = PublicationBindingOneOf | PublicationBindingOneOf1 | PublicationBindingOneOf2;

/**
 *
 * @export
 * @interface PublicationBindingOneOf
 */
export interface PublicationBindingOneOf {
    /**
     *
     * @type {PublicationBindingOneOfStatusEnum}
     * @memberof PublicationBindingOneOf
     */
    status?: PublicationBindingOneOfStatusEnum;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf
     */
    git_commit?: null;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf
     */
    error_code?: null;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf
     */
    published_at?: null;
}


/**
 * @export
 */
export const PublicationBindingOneOfStatusEnum = {
    Pending: 'PENDING'
} as const;
export type PublicationBindingOneOfStatusEnum = typeof PublicationBindingOneOfStatusEnum[keyof typeof PublicationBindingOneOfStatusEnum];

/**
 *
 * @export
 * @interface PublicationBindingOneOf1
 */
export interface PublicationBindingOneOf1 {
    /**
     *
     * @type {PublicationBindingOneOf1StatusEnum}
     * @memberof PublicationBindingOneOf1
     */
    status?: PublicationBindingOneOf1StatusEnum;
    /**
     *
     * @type {string}
     * @memberof PublicationBindingOneOf1
     */
    git_commit?: string;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf1
     */
    error_code?: null;
    /**
     *
     * @type {string}
     * @memberof PublicationBindingOneOf1
     */
    published_at?: string;
}


/**
 * @export
 */
export const PublicationBindingOneOf1StatusEnum = {
    Published: 'PUBLISHED'
} as const;
export type PublicationBindingOneOf1StatusEnum = typeof PublicationBindingOneOf1StatusEnum[keyof typeof PublicationBindingOneOf1StatusEnum];

/**
 *
 * @export
 * @interface PublicationBindingOneOf2
 */
export interface PublicationBindingOneOf2 {
    /**
     *
     * @type {PublicationBindingOneOf2StatusEnum}
     * @memberof PublicationBindingOneOf2
     */
    status?: PublicationBindingOneOf2StatusEnum;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf2
     */
    git_commit?: null;
    /**
     *
     * @type {string}
     * @memberof PublicationBindingOneOf2
     */
    error_code?: string;
    /**
     *
     * @type {null}
     * @memberof PublicationBindingOneOf2
     */
    published_at?: null;
}


/**
 * @export
 */
export const PublicationBindingOneOf2StatusEnum = {
    RecoveryRequired: 'RECOVERY_REQUIRED',
    Closed: 'CLOSED'
} as const;
export type PublicationBindingOneOf2StatusEnum = typeof PublicationBindingOneOf2StatusEnum[keyof typeof PublicationBindingOneOf2StatusEnum];

/**
 *
 * @export
 * @interface PublishArticleRevisionResult
 */
export interface PublishArticleRevisionResult {
    /**
     *
     * @type {PublicationBinding}
     * @memberof PublishArticleRevisionResult
     */
    publication: PublicationBinding;
    /**
     *
     * @type {boolean}
     * @memberof PublishArticleRevisionResult
     */
    replayed: boolean;
}
/**
 * Frozen Artifact publication binding. The three identifiers are pairwise distinct and source_coverage has unique section keys.
 * @export
 * @interface PublishArtifactBinding
 */
export interface PublishArtifactBinding {
    /**
     *
     * @type {string}
     * @memberof PublishArtifactBinding
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactBinding
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactBinding
     */
    revision_id: string;
    /**
     *
     * @type {number}
     * @memberof PublishArtifactBinding
     */
    revision_no: number;
    /**
     *
     * @type {number}
     * @memberof PublishArtifactBinding
     */
    artifact_version: number;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactBinding
     */
    content_hash: string;
    /**
     *
     * @type {Array<PublishArtifactCoverage>}
     * @memberof PublishArtifactBinding
     */
    source_coverage: Array<PublishArtifactCoverage>;
    /**
     *
     * @type {PublishArtifactBindingSchemaVersionEnum}
     * @memberof PublishArtifactBinding
     */
    schema_version: PublishArtifactBindingSchemaVersionEnum;
}


/**
 * @export
 */
export const PublishArtifactBindingSchemaVersionEnum = {
    ArtifactPublicationV1: 'artifact-publication/v1'
} as const;
export type PublishArtifactBindingSchemaVersionEnum = typeof PublishArtifactBindingSchemaVersionEnum[keyof typeof PublishArtifactBindingSchemaVersionEnum];

/**
 *
 * @export
 * @interface PublishArtifactCoverage
 */
export interface PublishArtifactCoverage {
    /**
     *
     * @type {string}
     * @memberof PublishArtifactCoverage
     */
    section_key: string;
    /**
     *
     * @type {PublishArtifactCoverageStatusEnum}
     * @memberof PublishArtifactCoverage
     */
    status: PublishArtifactCoverageStatusEnum;
    /**
     *
     * @type {Array<ArtifactGap>}
     * @memberof PublishArtifactCoverage
     */
    gaps: Array<ArtifactGap>;
}


/**
 * @export
 */
export const PublishArtifactCoverageStatusEnum = {
    Covered: 'COVERED',
    Partial: 'PARTIAL',
    Gap: 'GAP'
} as const;
export type PublishArtifactCoverageStatusEnum = typeof PublishArtifactCoverageStatusEnum[keyof typeof PublishArtifactCoverageStatusEnum];

/**
 * A frozen Artifact publication request. Approval alone does not create a Document, write Git, or update the index.
 * @export
 * @interface PublishArtifactProposal
 */
export interface PublishArtifactProposal {
    /**
     *
     * @type {PublishArtifactProposalProposalTypeEnum}
     * @memberof PublishArtifactProposal
     */
    proposal_type: PublishArtifactProposalProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactProposal
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactProposal
     */
    workspace_id: string;
    /**
     *
     * @type {PublishArtifactProposalStatusEnum}
     * @memberof PublishArtifactProposal
     */
    status: PublishArtifactProposalStatusEnum;
    /**
     *
     * @type {PublishArtifactProposalRiskLevelEnum}
     * @memberof PublishArtifactProposal
     */
    risk_level: PublishArtifactProposalRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof PublishArtifactProposal
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof PublishArtifactProposal
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {PublishArtifactRevision}
     * @memberof PublishArtifactProposal
     */
    revision: PublishArtifactRevision;
    /**
     *
     * @type {NonFileApproval}
     * @memberof PublishArtifactProposal
     */
    approval: NonFileApproval | null;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactProposal
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactProposal
     */
    updated_at: string;
}


/**
 * @export
 */
export const PublishArtifactProposalProposalTypeEnum = {
    PublishArtifact: 'publish_artifact'
} as const;
export type PublishArtifactProposalProposalTypeEnum = typeof PublishArtifactProposalProposalTypeEnum[keyof typeof PublishArtifactProposalProposalTypeEnum];

/**
 * @export
 */
export const PublishArtifactProposalStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type PublishArtifactProposalStatusEnum = typeof PublishArtifactProposalStatusEnum[keyof typeof PublishArtifactProposalStatusEnum];

/**
 * @export
 */
export const PublishArtifactProposalRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type PublishArtifactProposalRiskLevelEnum = typeof PublishArtifactProposalRiskLevelEnum[keyof typeof PublishArtifactProposalRiskLevelEnum];

/**
 *
 * @export
 * @interface PublishArtifactRevision
 */
export interface PublishArtifactRevision {
    /**
     *
     * @type {string}
     * @memberof PublishArtifactRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof PublishArtifactRevision
     */
    revision_no: number;
    /**
     *
     * @type {PublishArtifactBinding}
     * @memberof PublishArtifactRevision
     */
    publication: PublishArtifactBinding;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof PublishArtifactRevision
     */
    created_at: string;
}
/**
 *
 * @export
 * @interface Question
 */
export interface Question {
    /**
     *
     * @type {string}
     * @memberof Question
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Question
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof Question
     */
    conversation_id: string;
    /**
     *
     * @type {QuestionModeEnum}
     * @memberof Question
     */
    mode: QuestionModeEnum;
    /**
     *
     * @type {number}
     * @memberof Question
     */
    ordinal: number;
    /**
     *
     * @type {number}
     * @memberof Question
     */
    context_through_ordinal: number;
    /**
     *
     * @type {string}
     * @memberof Question
     */
    question: string;
    /**
     *
     * @type {QuestionScope}
     * @memberof Question
     */
    scope: QuestionScope;
    /**
     *
     * @type {QuestionAnswerDepthEnum}
     * @memberof Question
     */
    answer_depth: QuestionAnswerDepthEnum;
    /**
     *
     * @type {QuestionOutputFormatEnum}
     * @memberof Question
     */
    output_format: QuestionOutputFormatEnum;
    /**
     *
     * @type {string}
     * @memberof Question
     */
    created_at: string;
}


/**
 * @export
 */
export const QuestionModeEnum = {
    Rag: 'rag',
    WorkspaceAnalysis: 'workspace_analysis'
} as const;
export type QuestionModeEnum = typeof QuestionModeEnum[keyof typeof QuestionModeEnum];

/**
 * @export
 */
export const QuestionAnswerDepthEnum = {
    Concise: 'concise',
    Standard: 'standard',
    Detailed: 'detailed'
} as const;
export type QuestionAnswerDepthEnum = typeof QuestionAnswerDepthEnum[keyof typeof QuestionAnswerDepthEnum];

/**
 * @export
 */
export const QuestionOutputFormatEnum = {
    Markdown: 'markdown',
    Outline: 'outline'
} as const;
export type QuestionOutputFormatEnum = typeof QuestionOutputFormatEnum[keyof typeof QuestionOutputFormatEnum];

/**
 *
 * @export
 * @interface QuestionAcceptance
 */
export interface QuestionAcceptance {
    /**
     *
     * @type {Question}
     * @memberof QuestionAcceptance
     */
    question: Question;
    /**
     *
     * @type {Answer}
     * @memberof QuestionAcceptance
     */
    answer: Answer;
    /**
     *
     * @type {string}
     * @memberof QuestionAcceptance
     */
    status_url: string;
}
/**
 *
 * @export
 * @interface QuestionScope
 */
export interface QuestionScope {
    /**
     *
     * @type {QuestionScopeRetrievalModeEnum}
     * @memberof QuestionScope
     */
    retrieval_mode: QuestionScopeRetrievalModeEnum;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScope
     */
    source_ids: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScope
     */
    source_version_ids: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScope
     */
    path_prefixes: Array<string>;
    /**
     *
     * @type {string}
     * @memberof QuestionScope
     */
    captured_at_from: string | null;
    /**
     *
     * @type {string}
     * @memberof QuestionScope
     */
    captured_at_before: string | null;
    /**
     *
     * @type {boolean}
     * @memberof QuestionScope
     */
    allow_original_sources: boolean;
    /**
     *
     * @type {boolean}
     * @memberof QuestionScope
     */
    allow_web: boolean;
}


/**
 * @export
 */
export const QuestionScopeRetrievalModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type QuestionScopeRetrievalModeEnum = typeof QuestionScopeRetrievalModeEnum[keyof typeof QuestionScopeRetrievalModeEnum];

/**
 *
 * @export
 * @interface QuestionScopeRequest
 */
export interface QuestionScopeRequest {
    /**
     *
     * @type {QuestionScopeRequestRetrievalModeEnum}
     * @memberof QuestionScopeRequest
     */
    retrieval_mode?: QuestionScopeRequestRetrievalModeEnum;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScopeRequest
     */
    source_ids?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScopeRequest
     */
    source_version_ids?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof QuestionScopeRequest
     */
    path_prefixes?: Array<string>;
    /**
     *
     * @type {string}
     * @memberof QuestionScopeRequest
     */
    captured_at_from?: string | null;
    /**
     *
     * @type {string}
     * @memberof QuestionScopeRequest
     */
    captured_at_before?: string | null;
    /**
     *
     * @type {boolean}
     * @memberof QuestionScopeRequest
     */
    allow_original_sources?: boolean;
    /**
     *
     * @type {boolean}
     * @memberof QuestionScopeRequest
     */
    allow_web?: boolean;
}


/**
 * @export
 */
export const QuestionScopeRequestRetrievalModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type QuestionScopeRequestRetrievalModeEnum = typeof QuestionScopeRequestRetrievalModeEnum[keyof typeof QuestionScopeRequestRetrievalModeEnum];

/**
 *
 * @export
 * @interface RAGAnswerPayload
 */
export interface RAGAnswerPayload {
    /**
     *
     * @type {string}
     * @memberof RAGAnswerPayload
     */
    conclusion: string;
    /**
     *
     * @type {Array<RAGResultCitation>}
     * @memberof RAGAnswerPayload
     */
    citations: Array<RAGResultCitation>;
    /**
     *
     * @type {Array<RelatedTopic>}
     * @memberof RAGAnswerPayload
     */
    related_topics: Array<RelatedTopic>;
    /**
     *
     * @type {Array<string>}
     * @memberof RAGAnswerPayload
     */
    follow_up_questions: Array<string>;
    /**
     *
     * @type {Array<RAGAssertion>}
     * @memberof RAGAnswerPayload
     */
    assertions: Array<RAGAssertion>;
    /**
     *
     * @type {Array<RAGConflictPosition>}
     * @memberof RAGAnswerPayload
     */
    conflict_positions: Array<RAGConflictPosition>;
    /**
     *
     * @type {string}
     * @memberof RAGAnswerPayload
     */
    conflict_summary: string;
}
/**
 *
 * @export
 * @interface RAGAnswerResult
 */
export interface RAGAnswerResult {
    /**
     *
     * @type {RAGAnswerResultResultTypeEnum}
     * @memberof RAGAnswerResult
     */
    result_type: RAGAnswerResultResultTypeEnum;
    /**
     *
     * @type {RAGAnswerResultSchemaIdEnum}
     * @memberof RAGAnswerResult
     */
    schema_id: RAGAnswerResultSchemaIdEnum;
    /**
     *
     * @type {RAGAnswerResultSchemaVersionEnum}
     * @memberof RAGAnswerResult
     */
    schema_version: RAGAnswerResultSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof RAGAnswerResult
     */
    model_run_ref: string;
    /**
     *
     * @type {RAGAnswerPayload}
     * @memberof RAGAnswerResult
     */
    payload: RAGAnswerPayload;
}


/**
 * @export
 */
export const RAGAnswerResultResultTypeEnum = {
    RagAnswer: 'rag_answer'
} as const;
export type RAGAnswerResultResultTypeEnum = typeof RAGAnswerResultResultTypeEnum[keyof typeof RAGAnswerResultResultTypeEnum];

/**
 * @export
 */
export const RAGAnswerResultSchemaIdEnum = {
    AgentRagAnswer: 'agent.rag-answer'
} as const;
export type RAGAnswerResultSchemaIdEnum = typeof RAGAnswerResultSchemaIdEnum[keyof typeof RAGAnswerResultSchemaIdEnum];

/**
 * @export
 */
export const RAGAnswerResultSchemaVersionEnum = {
    V2: 'v2'
} as const;
export type RAGAnswerResultSchemaVersionEnum = typeof RAGAnswerResultSchemaVersionEnum[keyof typeof RAGAnswerResultSchemaVersionEnum];

/**
 *
 * @export
 * @interface RAGAssertion
 */
export interface RAGAssertion {
    /**
     *
     * @type {string}
     * @memberof RAGAssertion
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof RAGAssertion
     */
    text: string;
    /**
     *
     * @type {RAGAssertionKindEnum}
     * @memberof RAGAssertion
     */
    kind: RAGAssertionKindEnum;
    /**
     *
     * @type {Array<string>}
     * @memberof RAGAssertion
     */
    citation_ids: Array<string>;
}


/**
 * @export
 */
export const RAGAssertionKindEnum = {
    Factual: 'FACTUAL',
    ModelInference: 'MODEL_INFERENCE'
} as const;
export type RAGAssertionKindEnum = typeof RAGAssertionKindEnum[keyof typeof RAGAssertionKindEnum];

/**
 *
 * @export
 * @interface RAGCapabilityStatus
 */
export interface RAGCapabilityStatus {
    /**
     *
     * @type {RAGCapabilityStatusStatusEnum}
     * @memberof RAGCapabilityStatus
     */
    status: RAGCapabilityStatusStatusEnum;
    /**
     *
     * @type {RAGCapabilityStatusReasonEnum}
     * @memberof RAGCapabilityStatus
     */
    reason?: RAGCapabilityStatusReasonEnum;
}


/**
 * @export
 */
export const RAGCapabilityStatusStatusEnum = {
    Ready: 'ready',
    Disabled: 'disabled',
    Unavailable: 'unavailable'
} as const;
export type RAGCapabilityStatusStatusEnum = typeof RAGCapabilityStatusStatusEnum[keyof typeof RAGCapabilityStatusStatusEnum];

/**
 * @export
 */
export const RAGCapabilityStatusReasonEnum = {
    RagDependenciesUnavailable: 'rag_dependencies_unavailable'
} as const;
export type RAGCapabilityStatusReasonEnum = typeof RAGCapabilityStatusReasonEnum[keyof typeof RAGCapabilityStatusReasonEnum];

/**
 *
 * @export
 * @interface RAGConflictPosition
 */
export interface RAGConflictPosition {
    /**
     *
     * @type {string}
     * @memberof RAGConflictPosition
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof RAGConflictPosition
     */
    position: string;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof RAGConflictPosition
     */
    applicability: { [key: string]: JSONValue; };
    /**
     *
     * @type {Array<string>}
     * @memberof RAGConflictPosition
     */
    citation_ids: Array<string>;
    /**
     *
     * @type {string}
     * @memberof RAGConflictPosition
     */
    updated_at: string;
}
/**
 *
 * @export
 * @interface RAGResultCitation
 */
export interface RAGResultCitation {
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    chunk_id: string;
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof RAGResultCitation
     */
    source_span_id: string;
}
/**
 *
 * @export
 * @interface Readiness
 */
export interface Readiness {
    /**
     *
     * @type {ReadinessStatusEnum}
     * @memberof Readiness
     */
    status: ReadinessStatusEnum;
}


/**
 * @export
 */
export const ReadinessStatusEnum = {
    Ready: 'ready'
} as const;
export type ReadinessStatusEnum = typeof ReadinessStatusEnum[keyof typeof ReadinessStatusEnum];

/**
 *
 * @export
 * @interface RefusalResult
 */
export interface RefusalResult {
    /**
     *
     * @type {RefusalResultResultTypeEnum}
     * @memberof RefusalResult
     */
    result_type: RefusalResultResultTypeEnum;
    /**
     *
     * @type {string}
     * @memberof RefusalResult
     */
    schema_id: string;
    /**
     *
     * @type {string}
     * @memberof RefusalResult
     */
    schema_version: string;
    /**
     *
     * @type {string}
     * @memberof RefusalResult
     */
    model_run_ref: string;
    /**
     *
     * @type {RefusalResultPayload}
     * @memberof RefusalResult
     */
    payload: RefusalResultPayload;
}


/**
 * @export
 */
export const RefusalResultResultTypeEnum = {
    Refusal: 'refusal'
} as const;
export type RefusalResultResultTypeEnum = typeof RefusalResultResultTypeEnum[keyof typeof RefusalResultResultTypeEnum];

/**
 *
 * @export
 * @interface RefusalResultPayload
 */
export interface RefusalResultPayload {
    /**
     *
     * @type {string}
     * @memberof RefusalResultPayload
     */
    reason_code: string;
    /**
     *
     * @type {string}
     * @memberof RefusalResultPayload
     */
    summary: string;
    /**
     *
     * @type {string}
     * @memberof RefusalResultPayload
     */
    retrieval_scope: string;
    /**
     *
     * @type {Array<string>}
     * @memberof RefusalResultPayload
     */
    missing_requirements: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof RefusalResultPayload
     */
    suggested_actions: Array<string>;
}
/**
 *
 * @export
 * @interface RelatedTopic
 */
export interface RelatedTopic {
    /**
     *
     * @type {string}
     * @memberof RelatedTopic
     */
    topic_id: string;
    /**
     *
     * @type {string}
     * @memberof RelatedTopic
     */
    name: string;
    /**
     *
     * @type {Array<string>}
     * @memberof RelatedTopic
     */
    citation_ids: Array<string>;
}
/**
 *
 * @export
 * @interface RemoveGitRemoteConfigRequest
 */
export interface RemoveGitRemoteConfigRequest {
    /**
     *
     * @type {number}
     * @memberof RemoveGitRemoteConfigRequest
     */
    expected_revision: number;
}
/**
 *
 * @export
 * @interface RerankScore
 */
export interface RerankScore {
    /**
     *
     * @type {number}
     * @memberof RerankScore
     */
    rank: number;
    /**
     *
     * @type {number}
     * @memberof RerankScore
     */
    score: number;
    /**
     *
     * @type {string}
     * @memberof RerankScore
     */
    model_version: string;
}
/**
 * Immutable server-derived restore provenance. target_commit and expected_head use the same Git object format and must differ; current and target content hashes must differ.
 * @export
 * @interface RestoreDocumentBinding
 */
export interface RestoreDocumentBinding {
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    target_commit: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    expected_head: string;
    /**
     *
     * @type {number}
     * @memberof RestoreDocumentBinding
     */
    expected_document_version: number;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    preview_hash: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    current_content_hash: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentBinding
     */
    target_content_hash: string;
    /**
     *
     * @type {RestoreDocumentBindingSchemaVersionEnum}
     * @memberof RestoreDocumentBinding
     */
    schema_version: RestoreDocumentBindingSchemaVersionEnum;
}


/**
 * @export
 */
export const RestoreDocumentBindingSchemaVersionEnum = {
    DocumentRestoreV1: 'document-restore/v1'
} as const;
export type RestoreDocumentBindingSchemaVersionEnum = typeof RestoreDocumentBindingSchemaVersionEnum[keyof typeof RestoreDocumentBindingSchemaVersionEnum];

/**
 *
 * @export
 * @interface RestoreDocumentProposal
 */
export interface RestoreDocumentProposal {
    /**
     *
     * @type {RestoreDocumentProposalProposalTypeEnum}
     * @memberof RestoreDocumentProposal
     */
    proposal_type: RestoreDocumentProposalProposalTypeEnum;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentProposal
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentProposal
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentProposal
     */
    target_path: string;
    /**
     *
     * @type {RestoreDocumentProposalStatusEnum}
     * @memberof RestoreDocumentProposal
     */
    status: RestoreDocumentProposalStatusEnum;
    /**
     *
     * @type {RestoreDocumentProposalRiskLevelEnum}
     * @memberof RestoreDocumentProposal
     */
    risk_level: RestoreDocumentProposalRiskLevelEnum;
    /**
     *
     * @type {number}
     * @memberof RestoreDocumentProposal
     */
    version: number;
    /**
     *
     * @type {ProposalRevisionCapability}
     * @memberof RestoreDocumentProposal
     */
    revision_capability: ProposalRevisionCapability;
    /**
     *
     * @type {RestoreDocumentRevision}
     * @memberof RestoreDocumentProposal
     */
    revision: RestoreDocumentRevision;
    /**
     *
     * @type {Approval}
     * @memberof RestoreDocumentProposal
     */
    approval: Approval | null;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentProposal
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentProposal
     */
    updated_at: string;
}


/**
 * @export
 */
export const RestoreDocumentProposalProposalTypeEnum = {
    RestoreDocument: 'restore_document'
} as const;
export type RestoreDocumentProposalProposalTypeEnum = typeof RestoreDocumentProposalProposalTypeEnum[keyof typeof RestoreDocumentProposalProposalTypeEnum];

/**
 * @export
 */
export const RestoreDocumentProposalStatusEnum = {
    Draft: 'draft',
    Validating: 'validating',
    ReadyForReview: 'ready_for_review',
    Approved: 'approved',
    Applying: 'applying',
    Applied: 'applied',
    Verifying: 'verifying',
    Completed: 'completed',
    Rejected: 'rejected',
    NeedsRevision: 'needs_revision',
    Deferred: 'deferred',
    ApplyFailed: 'apply_failed',
    VerifyFailed: 'verify_failed',
    RolledBack: 'rolled_back',
    Cancelled: 'cancelled'
} as const;
export type RestoreDocumentProposalStatusEnum = typeof RestoreDocumentProposalStatusEnum[keyof typeof RestoreDocumentProposalStatusEnum];

/**
 * @export
 */
export const RestoreDocumentProposalRiskLevelEnum = {
    High: 'HIGH'
} as const;
export type RestoreDocumentProposalRiskLevelEnum = typeof RestoreDocumentProposalRiskLevelEnum[keyof typeof RestoreDocumentProposalRiskLevelEnum];

/**
 * Safe Writeback revision whose content is the exact target blob read by the server and whose base_hash equals restore.current_content_hash.
 * @export
 * @interface RestoreDocumentRevision
 */
export interface RestoreDocumentRevision {
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof RestoreDocumentRevision
     */
    revision_no: number;
    /**
     *
     * @type {RestoreDocumentRevisionTargetModeEnum}
     * @memberof RestoreDocumentRevision
     */
    target_mode: RestoreDocumentRevisionTargetModeEnum;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    base_hash: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    content: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    evidence_summary: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    risk: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    rollback_plan: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    change_hash: string;
    /**
     *
     * @type {string}
     * @memberof RestoreDocumentRevision
     */
    created_at: string;
    /**
     *
     * @type {RestoreDocumentBinding}
     * @memberof RestoreDocumentRevision
     */
    restore: RestoreDocumentBinding;
}


/**
 * @export
 */
export const RestoreDocumentRevisionTargetModeEnum = {
    Replace: 'REPLACE'
} as const;
export type RestoreDocumentRevisionTargetModeEnum = typeof RestoreDocumentRevisionTargetModeEnum[keyof typeof RestoreDocumentRevisionTargetModeEnum];

/**
 *
 * @export
 * @interface RetrievalDegradation
 */
export interface RetrievalDegradation {
    /**
     *
     * @type {RetrievalDegradationCapabilityEnum}
     * @memberof RetrievalDegradation
     */
    capability: RetrievalDegradationCapabilityEnum;
    /**
     *
     * @type {string}
     * @memberof RetrievalDegradation
     */
    code: string;
    /**
     *
     * @type {boolean}
     * @memberof RetrievalDegradation
     */
    retryable: boolean;
}


/**
 * @export
 */
export const RetrievalDegradationCapabilityEnum = {
    Vector: 'vector',
    Rerank: 'rerank'
} as const;
export type RetrievalDegradationCapabilityEnum = typeof RetrievalDegradationCapabilityEnum[keyof typeof RetrievalDegradationCapabilityEnum];

/**
 *
 * @export
 * @interface RetrievalScopeSummary
 */
export interface RetrievalScopeSummary {
    /**
     *
     * @type {Array<string>}
     * @memberof RetrievalScopeSummary
     */
    source_ids: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof RetrievalScopeSummary
     */
    source_version_ids: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof RetrievalScopeSummary
     */
    path_prefixes: Array<string>;
    /**
     *
     * @type {string}
     * @memberof RetrievalScopeSummary
     */
    captured_at_from: string | null;
    /**
     *
     * @type {string}
     * @memberof RetrievalScopeSummary
     */
    captured_at_before: string | null;
    /**
     *
     * @type {boolean}
     * @memberof RetrievalScopeSummary
     */
    allow_original_sources: boolean;
    /**
     *
     * @type {boolean}
     * @memberof RetrievalScopeSummary
     */
    allow_web: boolean;
}
/**
 *
 * @export
 * @interface RetrievalSummary
 */
export interface RetrievalSummary {
    /**
     *
     * @type {Array<string>}
     * @memberof RetrievalSummary
     */
    rewrites: Array<string>;
    /**
     *
     * @type {RetrievalSummaryRequestedModeEnum}
     * @memberof RetrievalSummary
     */
    requested_mode: RetrievalSummaryRequestedModeEnum;
    /**
     *
     * @type {RetrievalSummaryEffectiveModeEnum}
     * @memberof RetrievalSummary
     */
    effective_mode: RetrievalSummaryEffectiveModeEnum;
    /**
     *
     * @type {RetrievalScopeSummary}
     * @memberof RetrievalSummary
     */
    scope: RetrievalScopeSummary;
    /**
     *
     * @type {string}
     * @memberof RetrievalSummary
     */
    index_version_id?: string | null;
    /**
     *
     * @type {string}
     * @memberof RetrievalSummary
     */
    embedding_version_id?: string | null;
    /**
     *
     * @type {number}
     * @memberof RetrievalSummary
     */
    candidate_count: number;
    /**
     *
     * @type {number}
     * @memberof RetrievalSummary
     */
    selected_count: number;
    /**
     *
     * @type {number}
     * @memberof RetrievalSummary
     */
    conflict_count: number;
    /**
     *
     * @type {Array<RetrievalDegradation>}
     * @memberof RetrievalSummary
     */
    degradations: Array<RetrievalDegradation>;
}


/**
 * @export
 */
export const RetrievalSummaryRequestedModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type RetrievalSummaryRequestedModeEnum = typeof RetrievalSummaryRequestedModeEnum[keyof typeof RetrievalSummaryRequestedModeEnum];

/**
 * @export
 */
export const RetrievalSummaryEffectiveModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type RetrievalSummaryEffectiveModeEnum = typeof RetrievalSummaryEffectiveModeEnum[keyof typeof RetrievalSummaryEffectiveModeEnum];

/**
 *
 * @export
 * @interface RetryCaptureRequest
 */
export interface RetryCaptureRequest {
    /**
     *
     * @type {number}
     * @memberof RetryCaptureRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface RetryGitSyncRunRequest
 */
export interface RetryGitSyncRunRequest {
    /**
     *
     * @type {number}
     * @memberof RetryGitSyncRunRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface RetryKnowledgeProfileRequest
 */
export interface RetryKnowledgeProfileRequest {
    /**
     *
     * @type {number}
     * @memberof RetryKnowledgeProfileRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface ReviewAnswer
 */
export interface ReviewAnswer {
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    session_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    card_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    question_ref: string;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    user_answer: string;
    /**
     *
     * @type {ReviewAnswerRatingEnum}
     * @memberof ReviewAnswer
     */
    rating: ReviewAnswerRatingEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    scorer_version: string;
    /**
     *
     * @type {ReviewScore}
     * @memberof ReviewAnswer
     */
    score: ReviewScore;
    /**
     *
     * @type {{ [key: string]: JSONValue; }}
     * @memberof ReviewAnswer
     */
    feedback: { [key: string]: JSONValue; };
    /**
     *
     * @type {string}
     * @memberof ReviewAnswer
     */
    created_at: string;
}


/**
 * @export
 */
export const ReviewAnswerRatingEnum = {
    NUMBER_1: 1,
    NUMBER_2: 2,
    NUMBER_3: 3,
    NUMBER_4: 4
} as const;
export type ReviewAnswerRatingEnum = typeof ReviewAnswerRatingEnum[keyof typeof ReviewAnswerRatingEnum];

/**
 *
 * @export
 * @interface ReviewAnswerResult
 */
export interface ReviewAnswerResult {
    /**
     *
     * @type {ReviewAnswer}
     * @memberof ReviewAnswerResult
     */
    answer: ReviewAnswer;
    /**
     *
     * @type {ReviewSchedule}
     * @memberof ReviewAnswerResult
     */
    schedule: ReviewSchedule;
    /**
     *
     * @type {boolean}
     * @memberof ReviewAnswerResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ReviewCard
 */
export interface ReviewCard {
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    deck_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    question: string;
    /**
     *
     * @type {Array<string>}
     * @memberof ReviewCard
     */
    answer_points: Array<string>;
    /**
     *
     * @type {Array<ReviewEvidenceBinding>}
     * @memberof ReviewCard
     */
    evidence: Array<ReviewEvidenceBinding>;
    /**
     *
     * @type {ReviewCardCardTypeEnum}
     * @memberof ReviewCard
     */
    card_type: ReviewCardCardTypeEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewCard
     */
    difficulty: number;
    /**
     *
     * @type {ReviewCardStatusEnum}
     * @memberof ReviewCard
     */
    status: ReviewCardStatusEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    model_version: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    invalidation_reason?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    invalidated_at?: string;
    /**
     *
     * @type {number}
     * @memberof ReviewCard
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCard
     */
    updated_at: string;
}


/**
 * @export
 */
export const ReviewCardCardTypeEnum = {
    ShortAnswer: 'SHORT_ANSWER',
    Cloze: 'CLOZE',
    Comparison: 'COMPARISON',
    Scenario: 'SCENARIO',
    CodeReading: 'CODE_READING',
    Design: 'DESIGN'
} as const;
export type ReviewCardCardTypeEnum = typeof ReviewCardCardTypeEnum[keyof typeof ReviewCardCardTypeEnum];

/**
 * @export
 */
export const ReviewCardStatusEnum = {
    Draft: 'DRAFT',
    Approved: 'APPROVED',
    Invalidated: 'INVALIDATED',
    Rejected: 'REJECTED'
} as const;
export type ReviewCardStatusEnum = typeof ReviewCardStatusEnum[keyof typeof ReviewCardStatusEnum];

/**
 *
 * @export
 * @interface ReviewCardDecisionRequest
 */
export interface ReviewCardDecisionRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewCardDecisionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewCardDecisionRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewCardDecisionRequest
     */
    reason?: string;
}
/**
 *
 * @export
 * @interface ReviewCardEventOwnerBinding
 */
export interface ReviewCardEventOwnerBinding {
    /**
     *
     * @type {ReviewCardEventOwnerBindingReviewCard}
     * @memberof ReviewCardEventOwnerBinding
     */
    review_card: ReviewCardEventOwnerBindingReviewCard;
}
/**
 *
 * @export
 * @interface ReviewCardEventOwnerBindingReviewCard
 */
export interface ReviewCardEventOwnerBindingReviewCard {
    /**
     *
     * @type {string}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    card_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    card_version: number;
    /**
     *
     * @type {ReviewCardEventOwnerBindingReviewCardStatusEnum}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    status: ReviewCardEventOwnerBindingReviewCardStatusEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardEventOwnerBindingReviewCard
     */
    evidence_binding_fingerprint: string;
}


/**
 * @export
 */
export const ReviewCardEventOwnerBindingReviewCardStatusEnum = {
    Invalidated: 'INVALIDATED'
} as const;
export type ReviewCardEventOwnerBindingReviewCardStatusEnum = typeof ReviewCardEventOwnerBindingReviewCardStatusEnum[keyof typeof ReviewCardEventOwnerBindingReviewCardStatusEnum];

/**
 *
 * @export
 * @interface ReviewCardImpactBinding
 */
export interface ReviewCardImpactBinding {
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactBinding
     */
    card_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewCardImpactBinding
     */
    card_version: number;
    /**
     *
     * @type {ReviewCardImpactBindingStatusEnum}
     * @memberof ReviewCardImpactBinding
     */
    status: ReviewCardImpactBindingStatusEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactBinding
     */
    fingerprint: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactBinding
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactBinding
     */
    evidence_binding_fingerprint: string;
}


/**
 * @export
 */
export const ReviewCardImpactBindingStatusEnum = {
    Draft: 'DRAFT',
    Approved: 'APPROVED',
    Invalidated: 'INVALIDATED',
    Rejected: 'REJECTED'
} as const;
export type ReviewCardImpactBindingStatusEnum = typeof ReviewCardImpactBindingStatusEnum[keyof typeof ReviewCardImpactBindingStatusEnum];

/**
 *
 * @export
 * @interface ReviewCardImpactObject
 */
export interface ReviewCardImpactObject {
    /**
     *
     * @type {ReviewCardImpactObjectTypeEnum}
     * @memberof ReviewCardImpactObject
     */
    type: ReviewCardImpactObjectTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactObject
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactObject
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewCardImpactObject
     */
    version: number;
    /**
     *
     * @type {ReviewCardImpactObjectActionEnum}
     * @memberof ReviewCardImpactObject
     */
    action: ReviewCardImpactObjectActionEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewCardImpactObject
     */
    reason: string;
    /**
     *
     * @type {ReviewCardImpactObjectRequiresProposalEnum}
     * @memberof ReviewCardImpactObject
     */
    requires_proposal: ReviewCardImpactObjectRequiresProposalEnum;
    /**
     *
     * @type {ReviewCardImpactBinding}
     * @memberof ReviewCardImpactObject
     */
    review_card_binding: ReviewCardImpactBinding;
}


/**
 * @export
 */
export const ReviewCardImpactObjectTypeEnum = {
    ReviewCard: 'REVIEW_CARD'
} as const;
export type ReviewCardImpactObjectTypeEnum = typeof ReviewCardImpactObjectTypeEnum[keyof typeof ReviewCardImpactObjectTypeEnum];

/**
 * @export
 */
export const ReviewCardImpactObjectActionEnum = {
    RevalidateReviewCard: 'REVALIDATE_REVIEW_CARD'
} as const;
export type ReviewCardImpactObjectActionEnum = typeof ReviewCardImpactObjectActionEnum[keyof typeof ReviewCardImpactObjectActionEnum];

/**
 * @export
 */
export const ReviewCardImpactObjectRequiresProposalEnum = {
    True: true
} as const;
export type ReviewCardImpactObjectRequiresProposalEnum = typeof ReviewCardImpactObjectRequiresProposalEnum[keyof typeof ReviewCardImpactObjectRequiresProposalEnum];

/**
 *
 * @export
 * @interface ReviewCardList
 */
export interface ReviewCardList {
    /**
     *
     * @type {string}
     * @memberof ReviewCardList
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCardList
     */
    deck_id: string;
    /**
     *
     * @type {Array<ReviewCard>}
     * @memberof ReviewCardList
     */
    items: Array<ReviewCard>;
}
/**
 *
 * @export
 * @interface ReviewCompleteSessionRequest
 */
export interface ReviewCompleteSessionRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewCompleteSessionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {boolean}
     * @memberof ReviewCompleteSessionRequest
     */
    cancelled?: boolean;
}
/**
 *
 * @export
 * @interface ReviewCreateCardRequest
 */
export interface ReviewCreateCardRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewCreateCardRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCreateCardRequest
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCreateCardRequest
     */
    question: string;
    /**
     *
     * @type {Array<string>}
     * @memberof ReviewCreateCardRequest
     */
    answer_points: Array<string>;
    /**
     *
     * @type {Array<ReviewEvidenceBinding>}
     * @memberof ReviewCreateCardRequest
     */
    evidence: Array<ReviewEvidenceBinding>;
    /**
     *
     * @type {ReviewCreateCardRequestCardTypeEnum}
     * @memberof ReviewCreateCardRequest
     */
    card_type: ReviewCreateCardRequestCardTypeEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewCreateCardRequest
     */
    difficulty: number;
    /**
     *
     * @type {string}
     * @memberof ReviewCreateCardRequest
     */
    model_version?: string;
}


/**
 * @export
 */
export const ReviewCreateCardRequestCardTypeEnum = {
    ShortAnswer: 'SHORT_ANSWER',
    Cloze: 'CLOZE',
    Comparison: 'COMPARISON',
    Scenario: 'SCENARIO',
    CodeReading: 'CODE_READING',
    Design: 'DESIGN'
} as const;
export type ReviewCreateCardRequestCardTypeEnum = typeof ReviewCreateCardRequestCardTypeEnum[keyof typeof ReviewCreateCardRequestCardTypeEnum];

/**
 *
 * @export
 * @interface ReviewCreateDeckRequest
 */
export interface ReviewCreateDeckRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewCreateDeckRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewCreateDeckRequest
     */
    name: string;
    /**
     *
     * @type {object}
     * @memberof ReviewCreateDeckRequest
     */
    scope?: object;
    /**
     *
     * @type {number}
     * @memberof ReviewCreateDeckRequest
     */
    daily_limit?: number;
}
/**
 *
 * @export
 * @interface ReviewDeck
 */
export interface ReviewDeck {
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    name: string;
    /**
     *
     * @type {object}
     * @memberof ReviewDeck
     */
    scope: object;
    /**
     *
     * @type {ReviewDeckStatusEnum}
     * @memberof ReviewDeck
     */
    status: ReviewDeckStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewDeck
     */
    daily_limit: number;
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    scheduler_version: string;
    /**
     *
     * @type {number}
     * @memberof ReviewDeck
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDeck
     */
    updated_at: string;
}


/**
 * @export
 */
export const ReviewDeckStatusEnum = {
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Archived: 'ARCHIVED'
} as const;
export type ReviewDeckStatusEnum = typeof ReviewDeckStatusEnum[keyof typeof ReviewDeckStatusEnum];

/**
 *
 * @export
 * @interface ReviewDeckList
 */
export interface ReviewDeckList {
    /**
     *
     * @type {string}
     * @memberof ReviewDeckList
     */
    workspace_id: string;
    /**
     *
     * @type {Array<ReviewDeck>}
     * @memberof ReviewDeckList
     */
    items: Array<ReviewDeck>;
}
/**
 *
 * @export
 * @interface ReviewDeckScheduleRequest
 */
export interface ReviewDeckScheduleRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewDeckScheduleRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewDeckScheduleRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface ReviewDueCard
 */
export interface ReviewDueCard {
    /**
     *
     * @type {ReviewDueCardSummary}
     * @memberof ReviewDueCard
     */
    card: ReviewDueCardSummary;
    /**
     *
     * @type {ReviewSchedule}
     * @memberof ReviewDueCard
     */
    schedule: ReviewSchedule;
    /**
     * Opaque server-signed binding of the active Review Session, current card content and schedule snapshot.
     * @type {string}
     * @memberof ReviewDueCard
     */
    question_ref: string;
}
/**
 *
 * @export
 * @interface ReviewDueCardList
 */
export interface ReviewDueCardList {
    /**
     *
     * @type {string}
     * @memberof ReviewDueCardList
     */
    workspace_id: string;
    /**
     *
     * @type {Array<ReviewDueCard>}
     * @memberof ReviewDueCardList
     */
    items: Array<ReviewDueCard>;
}
/**
 * Deliberately redacted card projection for the pre-answer due queue. It never contains answer points, evidence, claim identity, model metadata, invalidation metadata or card timestamps.
 * @export
 * @interface ReviewDueCardSummary
 */
export interface ReviewDueCardSummary {
    /**
     *
     * @type {string}
     * @memberof ReviewDueCardSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDueCardSummary
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDueCardSummary
     */
    deck_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewDueCardSummary
     */
    question: string;
    /**
     *
     * @type {ReviewDueCardSummaryCardTypeEnum}
     * @memberof ReviewDueCardSummary
     */
    card_type: ReviewDueCardSummaryCardTypeEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewDueCardSummary
     */
    difficulty: number;
    /**
     *
     * @type {ReviewDueCardSummaryStatusEnum}
     * @memberof ReviewDueCardSummary
     */
    status: ReviewDueCardSummaryStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewDueCardSummary
     */
    version: number;
}


/**
 * @export
 */
export const ReviewDueCardSummaryCardTypeEnum = {
    ShortAnswer: 'SHORT_ANSWER',
    Cloze: 'CLOZE',
    Comparison: 'COMPARISON',
    Scenario: 'SCENARIO',
    CodeReading: 'CODE_READING',
    Design: 'DESIGN'
} as const;
export type ReviewDueCardSummaryCardTypeEnum = typeof ReviewDueCardSummaryCardTypeEnum[keyof typeof ReviewDueCardSummaryCardTypeEnum];

/**
 * @export
 */
export const ReviewDueCardSummaryStatusEnum = {
    Approved: 'APPROVED'
} as const;
export type ReviewDueCardSummaryStatusEnum = typeof ReviewDueCardSummaryStatusEnum[keyof typeof ReviewDueCardSummaryStatusEnum];

/**
 *
 * @export
 * @interface ReviewEditCardRequest
 */
export interface ReviewEditCardRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewEditCardRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewEditCardRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewEditCardRequest
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewEditCardRequest
     */
    question: string;
    /**
     *
     * @type {Array<string>}
     * @memberof ReviewEditCardRequest
     */
    answer_points: Array<string>;
    /**
     *
     * @type {Array<ReviewEvidenceBinding>}
     * @memberof ReviewEditCardRequest
     */
    evidence: Array<ReviewEvidenceBinding>;
    /**
     *
     * @type {ReviewEditCardRequestCardTypeEnum}
     * @memberof ReviewEditCardRequest
     */
    card_type: ReviewEditCardRequestCardTypeEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewEditCardRequest
     */
    difficulty: number;
    /**
     *
     * @type {string}
     * @memberof ReviewEditCardRequest
     */
    model_version?: string;
}


/**
 * @export
 */
export const ReviewEditCardRequestCardTypeEnum = {
    ShortAnswer: 'SHORT_ANSWER',
    Cloze: 'CLOZE',
    Comparison: 'COMPARISON',
    Scenario: 'SCENARIO',
    CodeReading: 'CODE_READING',
    Design: 'DESIGN'
} as const;
export type ReviewEditCardRequestCardTypeEnum = typeof ReviewEditCardRequestCardTypeEnum[keyof typeof ReviewEditCardRequestCardTypeEnum];

/**
 *
 * @export
 * @interface ReviewEvidenceBinding
 */
export interface ReviewEvidenceBinding {
    /**
     *
     * @type {ReviewEvidenceBindingSchemaVersionEnum}
     * @memberof ReviewEvidenceBinding
     */
    schema_version: ReviewEvidenceBindingSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewEvidenceBinding
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewEvidenceBinding
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewEvidenceBinding
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewEvidenceBinding
     */
    evidence_hash: string;
}


/**
 * @export
 */
export const ReviewEvidenceBindingSchemaVersionEnum = {
    ReviewEvidenceV1: 'review-evidence/v1'
} as const;
export type ReviewEvidenceBindingSchemaVersionEnum = typeof ReviewEvidenceBindingSchemaVersionEnum[keyof typeof ReviewEvidenceBindingSchemaVersionEnum];

/**
 *
 * @export
 * @interface ReviewInvalidationRequest
 */
export interface ReviewInvalidationRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationRequest
     */
    claim_id?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationRequest
     */
    source_version_id?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationRequest
     */
    source_span_id?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationRequest
     */
    reason: string;
}
/**
 * Bounded invalidation batch. When has_more is true, repeat the same selector with a new Idempotency-Key until it becomes false.
 * @export
 * @interface ReviewInvalidationResult
 */
export interface ReviewInvalidationResult {
    /**
     *
     * @type {string}
     * @memberof ReviewInvalidationResult
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewInvalidationResult
     */
    invalidated_count: number;
    /**
     *
     * @type {boolean}
     * @memberof ReviewInvalidationResult
     */
    has_more: boolean;
    /**
     *
     * @type {boolean}
     * @memberof ReviewInvalidationResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ReviewLearningPath
 */
export interface ReviewLearningPath {
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    workspace_id: string;
    /**
     *
     * @type {ReviewLearningPathOriginTypeEnum}
     * @memberof ReviewLearningPath
     */
    origin_type: ReviewLearningPathOriginTypeEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    review_answer_id: string;
    /**
     *
     * @type {LearningPathArtifactBinding}
     * @memberof ReviewLearningPath
     */
    artifact: LearningPathArtifactBinding;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    source_policy_version: string;
    /**
     *
     * @type {ReviewLearningPathStatusEnum}
     * @memberof ReviewLearningPath
     */
    status: ReviewLearningPathStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewLearningPath
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPath
     */
    updated_at: string;
}


/**
 * @export
 */
export const ReviewLearningPathOriginTypeEnum = {
    Review: 'REVIEW'
} as const;
export type ReviewLearningPathOriginTypeEnum = typeof ReviewLearningPathOriginTypeEnum[keyof typeof ReviewLearningPathOriginTypeEnum];

/**
 * @export
 */
export const ReviewLearningPathStatusEnum = {
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Completed: 'COMPLETED'
} as const;
export type ReviewLearningPathStatusEnum = typeof ReviewLearningPathStatusEnum[keyof typeof ReviewLearningPathStatusEnum];

/**
 *
 * @export
 * @interface ReviewLearningPathResult
 */
export interface ReviewLearningPathResult {
    /**
     *
     * @type {ReviewLearningPath}
     * @memberof ReviewLearningPathResult
     */
    path: ReviewLearningPath;
    /**
     *
     * @type {Array<ReviewLearningPathStep>}
     * @memberof ReviewLearningPathResult
     */
    steps: Array<ReviewLearningPathStep>;
    /**
     *
     * @type {boolean}
     * @memberof ReviewLearningPathResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ReviewLearningPathStatusResult
 */
export interface ReviewLearningPathStatusResult {
    /**
     *
     * @type {ReviewLearningPath}
     * @memberof ReviewLearningPathStatusResult
     */
    path: ReviewLearningPath;
    /**
     *
     * @type {boolean}
     * @memberof ReviewLearningPathStatusResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ReviewLearningPathStep
 */
export interface ReviewLearningPathStep {
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    path_id: string;
    /**
     *
     * @type {number}
     * @memberof ReviewLearningPathStep
     */
    step_no: number;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    topic_id?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    evidence_hash: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    rationale: string;
    /**
     *
     * @type {ReviewLearningPathStepStatusEnum}
     * @memberof ReviewLearningPathStep
     */
    status: ReviewLearningPathStepStatusEnum;
    /**
     *
     * @type {number}
     * @memberof ReviewLearningPathStep
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof ReviewLearningPathStep
     */
    updated_at: string;
}


/**
 * @export
 */
export const ReviewLearningPathStepStatusEnum = {
    Pending: 'PENDING',
    InProgress: 'IN_PROGRESS',
    Completed: 'COMPLETED',
    Skipped: 'SKIPPED'
} as const;
export type ReviewLearningPathStepStatusEnum = typeof ReviewLearningPathStepStatusEnum[keyof typeof ReviewLearningPathStepStatusEnum];

/**
 *
 * @export
 * @interface ReviewLearningPathStepResult
 */
export interface ReviewLearningPathStepResult {
    /**
     *
     * @type {ReviewLearningPath}
     * @memberof ReviewLearningPathStepResult
     */
    path: ReviewLearningPath;
    /**
     *
     * @type {ReviewLearningPathStep}
     * @memberof ReviewLearningPathStepResult
     */
    step: ReviewLearningPathStep;
    /**
     *
     * @type {boolean}
     * @memberof ReviewLearningPathStepResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface ReviewSchedule
 */
export interface ReviewSchedule {
    /**
     *
     * @type {string}
     * @memberof ReviewSchedule
     */
    card_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSchedule
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSchedule
     */
    due_at: string;
    /**
     *
     * @type {number}
     * @memberof ReviewSchedule
     */
    interval_days: number;
    /**
     *
     * @type {number}
     * @memberof ReviewSchedule
     */
    stability: number;
    /**
     *
     * @type {number}
     * @memberof ReviewSchedule
     */
    difficulty: number;
    /**
     *
     * @type {string}
     * @memberof ReviewSchedule
     */
    last_reviewed_at?: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSchedule
     */
    scheduler_version: string;
    /**
     *
     * @type {boolean}
     * @memberof ReviewSchedule
     */
    paused: boolean;
    /**
     *
     * @type {number}
     * @memberof ReviewSchedule
     */
    version: number;
}
/**
 *
 * @export
 * @interface ReviewScore
 */
export interface ReviewScore {
    /**
     *
     * @type {ReviewScoreSchemaVersionEnum}
     * @memberof ReviewScore
     */
    schema_version: ReviewScoreSchemaVersionEnum;
    /**
     *
     * @type {ReviewScoreDimension}
     * @memberof ReviewScore
     */
    correctness: ReviewScoreDimension;
    /**
     *
     * @type {ReviewScoreDimension}
     * @memberof ReviewScore
     */
    coverage: ReviewScoreDimension;
    /**
     *
     * @type {ReviewScoreDimension}
     * @memberof ReviewScore
     */
    boundaries: ReviewScoreDimension;
    /**
     *
     * @type {ReviewScoreDimension}
     * @memberof ReviewScore
     */
    clarity: ReviewScoreDimension;
    /**
     *
     * @type {ReviewScoreDimension}
     * @memberof ReviewScore
     */
    confidence: ReviewScoreDimension;
    /**
     *
     * @type {Array<string>}
     * @memberof ReviewScore
     */
    errors?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof ReviewScore
     */
    omissions?: Array<string>;
    /**
     *
     * @type {Array<ReviewScoreEvidence>}
     * @memberof ReviewScore
     */
    evidence: Array<ReviewScoreEvidence>;
}


/**
 * @export
 */
export const ReviewScoreSchemaVersionEnum = {
    ReviewScoreV1: 'review-score/v1'
} as const;
export type ReviewScoreSchemaVersionEnum = typeof ReviewScoreSchemaVersionEnum[keyof typeof ReviewScoreSchemaVersionEnum];

/**
 *
 * @export
 * @interface ReviewScoreDimension
 */
export interface ReviewScoreDimension {
    /**
     *
     * @type {number}
     * @memberof ReviewScoreDimension
     */
    value: number;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreDimension
     */
    rationale: string;
}
/**
 *
 * @export
 * @interface ReviewScoreEvidence
 */
export interface ReviewScoreEvidence {
    /**
     *
     * @type {ReviewScoreEvidenceSchemaVersionEnum}
     * @memberof ReviewScoreEvidence
     */
    schema_version: ReviewScoreEvidenceSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    claim_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    evidence_hash: string;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    source_version_href: string;
    /**
     *
     * @type {string}
     * @memberof ReviewScoreEvidence
     */
    source_span_href: string;
}


/**
 * @export
 */
export const ReviewScoreEvidenceSchemaVersionEnum = {
    ReviewEvidenceV1: 'review-evidence/v1'
} as const;
export type ReviewScoreEvidenceSchemaVersionEnum = typeof ReviewScoreEvidenceSchemaVersionEnum[keyof typeof ReviewScoreEvidenceSchemaVersionEnum];

/**
 *
 * @export
 * @interface ReviewSession
 */
export interface ReviewSession {
    /**
     *
     * @type {string}
     * @memberof ReviewSession
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSession
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSession
     */
    deck_id: string;
    /**
     *
     * @type {ReviewSessionSessionTypeEnum}
     * @memberof ReviewSession
     */
    session_type: ReviewSessionSessionTypeEnum;
    /**
     *
     * @type {ReviewSessionStatusEnum}
     * @memberof ReviewSession
     */
    status: ReviewSessionStatusEnum;
    /**
     *
     * @type {object}
     * @memberof ReviewSession
     */
    config: object;
    /**
     *
     * @type {string}
     * @memberof ReviewSession
     */
    started_at: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSession
     */
    ended_at?: string;
}


/**
 * @export
 */
export const ReviewSessionSessionTypeEnum = {
    Review: 'REVIEW'
} as const;
export type ReviewSessionSessionTypeEnum = typeof ReviewSessionSessionTypeEnum[keyof typeof ReviewSessionSessionTypeEnum];

/**
 * @export
 */
export const ReviewSessionStatusEnum = {
    Active: 'ACTIVE',
    Completed: 'COMPLETED',
    Cancelled: 'CANCELLED'
} as const;
export type ReviewSessionStatusEnum = typeof ReviewSessionStatusEnum[keyof typeof ReviewSessionStatusEnum];

/**
 *
 * @export
 * @interface ReviewStartSessionRequest
 */
export interface ReviewStartSessionRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewStartSessionRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewStartSessionRequest
     */
    deck_id: string;
    /**
     *
     * @type {ReviewStartSessionRequestSessionTypeEnum}
     * @memberof ReviewStartSessionRequest
     */
    session_type: ReviewStartSessionRequestSessionTypeEnum;
    /**
     *
     * @type {object}
     * @memberof ReviewStartSessionRequest
     */
    config?: object;
}


/**
 * @export
 */
export const ReviewStartSessionRequestSessionTypeEnum = {
    Review: 'REVIEW'
} as const;
export type ReviewStartSessionRequestSessionTypeEnum = typeof ReviewStartSessionRequestSessionTypeEnum[keyof typeof ReviewStartSessionRequestSessionTypeEnum];

/**
 *
 * @export
 * @interface ReviewSubmitAnswerRequest
 */
export interface ReviewSubmitAnswerRequest {
    /**
     *
     * @type {string}
     * @memberof ReviewSubmitAnswerRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSubmitAnswerRequest
     */
    card_id: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSubmitAnswerRequest
     */
    question_ref: string;
    /**
     *
     * @type {string}
     * @memberof ReviewSubmitAnswerRequest
     */
    user_answer: string;
    /**
     *
     * @type {ReviewSubmitAnswerRequestRatingEnum}
     * @memberof ReviewSubmitAnswerRequest
     */
    rating: ReviewSubmitAnswerRequestRatingEnum;
}


/**
 * @export
 */
export const ReviewSubmitAnswerRequestRatingEnum = {
    NUMBER_1: 1,
    NUMBER_2: 2,
    NUMBER_3: 3,
    NUMBER_4: 4
} as const;
export type ReviewSubmitAnswerRequestRatingEnum = typeof ReviewSubmitAnswerRequestRatingEnum[keyof typeof ReviewSubmitAnswerRequestRatingEnum];

/**
 *
 * @export
 * @interface SaveGitRemoteConfigRequest
 */
export interface SaveGitRemoteConfigRequest {
    /**
     *
     * @type {number}
     * @memberof SaveGitRemoteConfigRequest
     */
    expected_revision: number;
    /**
     * Remote input normalized by the server; after trimming it must be a public HTTPS repository URL without userinfo, query, or fragment.
     * @type {string}
     * @memberof SaveGitRemoteConfigRequest
     */
    remote_url: string;
    /**
     *
     * @type {string}
     * @memberof SaveGitRemoteConfigRequest
     */
    branch: string;
    /**
     *
     * @type {boolean}
     * @memberof SaveGitRemoteConfigRequest
     */
    auto_sync: boolean;
    /**
     *
     * @type {GitSyncTokenAction}
     * @memberof SaveGitRemoteConfigRequest
     */
    token: GitSyncTokenAction;
}
/**
 *
 * @export
 * @interface ScannedFile
 */
export interface ScannedFile {
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    relative_path: string;
    /**
     *
     * @type {number}
     * @memberof ScannedFile
     */
    byte_size: number;
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    media_type: string;
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    source_id: string;
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof ScannedFile
     */
    content_artifact_id: string;
    /**
     * True when this scan published the managed immutable bytes; false when an identical artifact was reused.
     * @type {boolean}
     * @memberof ScannedFile
     */
    content_artifact_created: boolean;
}
/**
 *
 * @export
 * @interface SearchDegradation
 */
export interface SearchDegradation {
    /**
     *
     * @type {SearchDegradationCapabilityEnum}
     * @memberof SearchDegradation
     */
    capability: SearchDegradationCapabilityEnum;
    /**
     *
     * @type {string}
     * @memberof SearchDegradation
     */
    error_code: string;
    /**
     *
     * @type {boolean}
     * @memberof SearchDegradation
     */
    retryable: boolean;
}


/**
 * @export
 */
export const SearchDegradationCapabilityEnum = {
    Vector: 'vector',
    Rerank: 'rerank'
} as const;
export type SearchDegradationCapabilityEnum = typeof SearchDegradationCapabilityEnum[keyof typeof SearchDegradationCapabilityEnum];

/**
 *
 * @export
 * @interface SearchEvidence
 */
export interface SearchEvidence {
    /**
     *
     * @type {string}
     * @memberof SearchEvidence
     */
    chunk_id: string;
    /**
     *
     * @type {string}
     * @memberof SearchEvidence
     */
    parse_projection_id: string;
    /**
     *
     * @type {number}
     * @memberof SearchEvidence
     */
    sequence: number;
    /**
     *
     * @type {string}
     * @memberof SearchEvidence
     */
    content_hash: string;
    /**
     *
     * @type {Array<string>}
     * @memberof SearchEvidence
     */
    heading_path: Array<string>;
    /**
     *
     * @type {EvidenceSpan}
     * @memberof SearchEvidence
     */
    span: EvidenceSpan;
    /**
     *
     * @type {string}
     * @memberof SearchEvidence
     */
    snippet: string;
    /**
     *
     * @type {Array<EvidenceProvenance>}
     * @memberof SearchEvidence
     */
    provenances: Array<EvidenceProvenance>;
    /**
     *
     * @type {boolean}
     * @memberof SearchEvidence
     */
    provenance_truncated: boolean;
    /**
     *
     * @type {EvidenceScores}
     * @memberof SearchEvidence
     */
    scores: EvidenceScores;
}
/**
 *
 * @export
 * @interface SearchFilter
 */
export interface SearchFilter {
    /**
     *
     * @type {Array<string>}
     * @memberof SearchFilter
     */
    source_ids?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof SearchFilter
     */
    source_version_ids?: Array<string>;
    /**
     *
     * @type {Array<string>}
     * @memberof SearchFilter
     */
    path_prefixes?: Array<string>;
    /**
     *
     * @type {string}
     * @memberof SearchFilter
     */
    captured_at_from?: string;
    /**
     *
     * @type {string}
     * @memberof SearchFilter
     */
    captured_at_before?: string;
}
/**
 *
 * @export
 * @interface SearchRequest
 */
export interface SearchRequest {
    /**
     *
     * @type {string}
     * @memberof SearchRequest
     */
    workspace_id: string;
    /**
     * After trimming leading and trailing whitespace, the query must be non-empty, valid UTF-8, contain no NUL code point, and use at most 8192 UTF-8 bytes. maxLength remains a conservative code-point bound; x-max-utf8-bytes is the authoritative byte limit.
     * @type {string}
     * @memberof SearchRequest
     */
    query: string;
    /**
     *
     * @type {SearchRequestRetrievalModeEnum}
     * @memberof SearchRequest
     */
    retrieval_mode?: SearchRequestRetrievalModeEnum;
    /**
     *
     * @type {SearchFilter}
     * @memberof SearchRequest
     */
    filters?: SearchFilter;
    /**
     * Opaque process-lifetime HMAC cursor bound to the canonical request, Active Index and complete top-100 result fingerprint. Clients restart from the first page after an invalid or stale cursor.
     * @type {string}
     * @memberof SearchRequest
     */
    cursor?: string;
    /**
     *
     * @type {number}
     * @memberof SearchRequest
     */
    limit?: number;
}


/**
 * @export
 */
export const SearchRequestRetrievalModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type SearchRequestRetrievalModeEnum = typeof SearchRequestRetrievalModeEnum[keyof typeof SearchRequestRetrievalModeEnum];

/**
 *
 * @export
 * @interface SearchResponse
 */
export interface SearchResponse {
    /**
     *
     * @type {string}
     * @memberof SearchResponse
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof SearchResponse
     */
    index_version_id: string;
    /**
     *
     * @type {string}
     * @memberof SearchResponse
     */
    embedding_version_id: string | null;
    /**
     *
     * @type {SearchResponseRequestedModeEnum}
     * @memberof SearchResponse
     */
    requested_mode: SearchResponseRequestedModeEnum;
    /**
     *
     * @type {SearchResponseEffectiveModeEnum}
     * @memberof SearchResponse
     */
    effective_mode: SearchResponseEffectiveModeEnum;
    /**
     *
     * @type {Array<SearchResponseIndexDegradedCapabilitiesEnum>}
     * @memberof SearchResponse
     */
    index_degraded_capabilities: Array<SearchResponseIndexDegradedCapabilitiesEnum>;
    /**
     *
     * @type {Array<SearchDegradation>}
     * @memberof SearchResponse
     */
    degradations: Array<SearchDegradation>;
    /**
     *
     * @type {Array<SearchEvidence>}
     * @memberof SearchResponse
     */
    items: Array<SearchEvidence>;
    /**
     * Opaque process-lifetime HMAC cursor bound to the canonical request, Active Index and complete top-100 result fingerprint. Clients restart from the first page after an invalid or stale cursor.
     * @type {string}
     * @memberof SearchResponse
     */
    next_cursor?: string;
}


/**
 * @export
 */
export const SearchResponseRequestedModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type SearchResponseRequestedModeEnum = typeof SearchResponseRequestedModeEnum[keyof typeof SearchResponseRequestedModeEnum];

/**
 * @export
 */
export const SearchResponseEffectiveModeEnum = {
    Keyword: 'keyword',
    Semantic: 'semantic',
    Hybrid: 'hybrid'
} as const;
export type SearchResponseEffectiveModeEnum = typeof SearchResponseEffectiveModeEnum[keyof typeof SearchResponseEffectiveModeEnum];

/**
 * @export
 */
export const SearchResponseIndexDegradedCapabilitiesEnum = {
    Vector: 'vector'
} as const;
export type SearchResponseIndexDegradedCapabilitiesEnum = typeof SearchResponseIndexDegradedCapabilitiesEnum[keyof typeof SearchResponseIndexDegradedCapabilitiesEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidate
 */
export interface SemanticLinkCandidate {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    fingerprint: string;
    /**
     *
     * @type {SemanticLinkCandidateStatus}
     * @memberof SemanticLinkCandidate
     */
    status: SemanticLinkCandidateStatus;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidate
     */
    version: number;
    /**
     *
     * @type {SemanticLinkCandidateEndpoint}
     * @memberof SemanticLinkCandidate
     */
    source: SemanticLinkCandidateEndpoint;
    /**
     *
     * @type {SemanticLinkCandidateEndpoint}
     * @memberof SemanticLinkCandidate
     */
    target: SemanticLinkCandidateEndpoint;
    /**
     *
     * @type {SemanticLinkRelationType}
     * @memberof SemanticLinkCandidate
     */
    proposed_relation_type: SemanticLinkRelationType;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidate
     */
    confidence: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    reason: string;
    /**
     *
     * @type {Array<SemanticLinkDiscoveryMethod>}
     * @memberof SemanticLinkCandidate
     */
    discovery_methods: Array<SemanticLinkDiscoveryMethod>;
    /**
     *
     * @type {Array<SemanticLinkCandidateEvidence>}
     * @memberof SemanticLinkCandidate
     */
    evidence: Array<SemanticLinkCandidateEvidence>;
    /**
     *
     * @type {SemanticLinkGeneration}
     * @memberof SemanticLinkCandidate
     */
    generation: SemanticLinkGeneration;
    /**
     *
     * @type {SemanticLinkCandidateReopenedReasonEnum}
     * @memberof SemanticLinkCandidate
     */
    reopened_reason: SemanticLinkCandidateReopenedReasonEnum | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    reopened_from_candidate_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    proposal_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    deferred_until: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidate
     */
    updated_at: string;
}


/**
 * @export
 */
export const SemanticLinkCandidateReopenedReasonEnum = {
    ContentChanged: 'CONTENT_CHANGED'
} as const;
export type SemanticLinkCandidateReopenedReasonEnum = typeof SemanticLinkCandidateReopenedReasonEnum[keyof typeof SemanticLinkCandidateReopenedReasonEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionReceipt
 */
export interface SemanticLinkCandidateDecisionReceipt {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    candidate_id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionReceiptActionEnum}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    action: SemanticLinkCandidateDecisionReceiptActionEnum;
    /**
     *
     * @type {SemanticLinkCandidateStatus}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    status: SemanticLinkCandidateStatus;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    proposal_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionReceipt
     */
    updated_at: string;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionReceiptActionEnum = {
    Confirm: 'CONFIRM',
    ConfirmWithRelationType: 'CONFIRM_WITH_RELATION_TYPE',
    Ignore: 'IGNORE',
    FalsePositive: 'FALSE_POSITIVE',
    Defer: 'DEFER',
    Resume: 'RESUME'
} as const;
export type SemanticLinkCandidateDecisionReceiptActionEnum = typeof SemanticLinkCandidateDecisionReceiptActionEnum[keyof typeof SemanticLinkCandidateDecisionReceiptActionEnum];

/**
 * @type SemanticLinkCandidateDecisionRequest
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type SemanticLinkCandidateDecisionRequest = SemanticLinkCandidateDecisionRequestOneOf | SemanticLinkCandidateDecisionRequestOneOf1 | SemanticLinkCandidateDecisionRequestOneOf2 | SemanticLinkCandidateDecisionRequestOneOf3 | SemanticLinkCandidateDecisionRequestOneOf4;

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionRequestOneOf
 */
export interface SemanticLinkCandidateDecisionRequestOneOf {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOfActionEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf
     */
    action: SemanticLinkCandidateDecisionRequestOneOfActionEnum;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf
     */
    expected_version: number;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOfActionEnum = {
    Confirm: 'CONFIRM'
} as const;
export type SemanticLinkCandidateDecisionRequestOneOfActionEnum = typeof SemanticLinkCandidateDecisionRequestOneOfActionEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOfActionEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionRequestOneOf1
 */
export interface SemanticLinkCandidateDecisionRequestOneOf1 {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf1
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOf1ActionEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf1
     */
    action: SemanticLinkCandidateDecisionRequestOneOf1ActionEnum;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf1
     */
    expected_version: number;
    /**
     *
     * @type {SemanticLinkRelationType}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf1
     */
    relation_type: SemanticLinkRelationType;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOf1ActionEnum = {
    ConfirmWithRelationType: 'CONFIRM_WITH_RELATION_TYPE'
} as const;
export type SemanticLinkCandidateDecisionRequestOneOf1ActionEnum = typeof SemanticLinkCandidateDecisionRequestOneOf1ActionEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOf1ActionEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionRequestOneOf2
 */
export interface SemanticLinkCandidateDecisionRequestOneOf2 {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf2
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOf2ActionEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf2
     */
    action: SemanticLinkCandidateDecisionRequestOneOf2ActionEnum;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf2
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf2
     */
    reason: string;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOf2ActionEnum = {
    Ignore: 'IGNORE',
    FalsePositive: 'FALSE_POSITIVE'
} as const;
export type SemanticLinkCandidateDecisionRequestOneOf2ActionEnum = typeof SemanticLinkCandidateDecisionRequestOneOf2ActionEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOf2ActionEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionRequestOneOf3
 */
export interface SemanticLinkCandidateDecisionRequestOneOf3 {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf3
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOf3ActionEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf3
     */
    action: SemanticLinkCandidateDecisionRequestOneOf3ActionEnum;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf3
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf3
     */
    deferred_until?: string | null;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOf3ActionEnum = {
    Defer: 'DEFER'
} as const;
export type SemanticLinkCandidateDecisionRequestOneOf3ActionEnum = typeof SemanticLinkCandidateDecisionRequestOneOf3ActionEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOf3ActionEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateDecisionRequestOneOf4
 */
export interface SemanticLinkCandidateDecisionRequestOneOf4 {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf4
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOf4ActionEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf4
     */
    action: SemanticLinkCandidateDecisionRequestOneOf4ActionEnum;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf4
     */
    expected_version: number;
    /**
     *
     * @type {SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum}
     * @memberof SemanticLinkCandidateDecisionRequestOneOf4
     */
    deferred_until?: SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum | null;
}


/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOf4ActionEnum = {
    Resume: 'RESUME'
} as const;
export type SemanticLinkCandidateDecisionRequestOneOf4ActionEnum = typeof SemanticLinkCandidateDecisionRequestOneOf4ActionEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOf4ActionEnum];

/**
 * @export
 */
export const SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum = {
} as const;
export type SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum = typeof SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum[keyof typeof SemanticLinkCandidateDecisionRequestOneOf4DeferredUntilEnum];

/**
 *
 * @export
 * @interface SemanticLinkCandidateEndpoint
 */
export interface SemanticLinkCandidateEndpoint {
    /**
     *
     * @type {SemanticLinkNodeType}
     * @memberof SemanticLinkCandidateEndpoint
     */
    type: SemanticLinkNodeType;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEndpoint
     */
    id: string;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkCandidateEndpoint
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEndpoint
     */
    summary: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEndpoint
     */
    excerpt: string;
}


/**
 *
 * @export
 * @interface SemanticLinkCandidateEvidence
 */
export interface SemanticLinkCandidateEvidence {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    semantic_hash: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    source_version_href: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    source_span_href: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    excerpt: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidateEvidence
     */
    reason: string;
}
/**
 *
 * @export
 * @interface SemanticLinkCandidatePage
 */
export interface SemanticLinkCandidatePage {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkCandidatePage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<SemanticLinkCandidate>}
     * @memberof SemanticLinkCandidatePage
     */
    items: Array<SemanticLinkCandidate>;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof SemanticLinkCandidatePage
     */
    next_cursor?: string;
}

/**
 *
 * @export
 */
export const SemanticLinkCandidateStatus = {
    Active: 'ACTIVE',
    Deferred: 'DEFERRED',
    Ignored: 'IGNORED',
    FalsePositive: 'FALSE_POSITIVE',
    ProposalCreated: 'PROPOSAL_CREATED',
    Superseded: 'SUPERSEDED'
} as const;
export type SemanticLinkCandidateStatus = typeof SemanticLinkCandidateStatus[keyof typeof SemanticLinkCandidateStatus];

/**
 *
 * @export
 * @interface SemanticLinkCapabilityStatus
 */
export interface SemanticLinkCapabilityStatus {
    /**
     *
     * @type {SemanticLinkCapabilityStatusStatusEnum}
     * @memberof SemanticLinkCapabilityStatus
     */
    status: SemanticLinkCapabilityStatusStatusEnum;
    /**
     *
     * @type {SemanticLinkCapabilityStatusReasonEnum}
     * @memberof SemanticLinkCapabilityStatus
     */
    reason?: SemanticLinkCapabilityStatusReasonEnum;
}


/**
 * @export
 */
export const SemanticLinkCapabilityStatusStatusEnum = {
    Ready: 'ready',
    Unavailable: 'unavailable'
} as const;
export type SemanticLinkCapabilityStatusStatusEnum = typeof SemanticLinkCapabilityStatusStatusEnum[keyof typeof SemanticLinkCapabilityStatusStatusEnum];

/**
 * @export
 */
export const SemanticLinkCapabilityStatusReasonEnum = {
    SemanticLinkDependenciesUnavailable: 'semantic_link_dependencies_unavailable'
} as const;
export type SemanticLinkCapabilityStatusReasonEnum = typeof SemanticLinkCapabilityStatusReasonEnum[keyof typeof SemanticLinkCapabilityStatusReasonEnum];


/**
 *
 * @export
 */
export const SemanticLinkDiscoveryMethod = {
    TitleAlias: 'TITLE_ALIAS',
    TermMatch: 'TERM_MATCH',
    ClaimSemanticSimilarity: 'CLAIM_SEMANTIC_SIMILARITY',
    CommonTopic: 'COMMON_TOPIC',
    SharedSource: 'SHARED_SOURCE',
    RagCoRetrieval: 'RAG_CO_RETRIEVAL'
} as const;
export type SemanticLinkDiscoveryMethod = typeof SemanticLinkDiscoveryMethod[keyof typeof SemanticLinkDiscoveryMethod];

/**
 *
 * @export
 * @interface SemanticLinkGeneration
 */
export interface SemanticLinkGeneration {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    index_version_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    embedding_version_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    rerank_version_id: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    model_version?: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    model_profile_version?: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    prompt_version?: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    schema_version?: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    model_run_id?: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    rule_id?: string | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkGeneration
     */
    rule_version?: string;
}

/**
 *
 * @export
 */
export const SemanticLinkNodeType = {
    Topic: 'TOPIC',
    Claim: 'CLAIM'
} as const;
export type SemanticLinkNodeType = typeof SemanticLinkNodeType[keyof typeof SemanticLinkNodeType];


/**
 *
 * @export
 */
export const SemanticLinkRelationType = {
    Cites: 'CITES',
    DerivedFrom: 'DERIVED_FROM',
    BelongsTo: 'BELONGS_TO',
    Supports: 'SUPPORTS',
    Complements: 'COMPLEMENTS',
    Duplicates: 'DUPLICATES',
    ConflictsWith: 'CONFLICTS_WITH',
    PrerequisiteOf: 'PREREQUISITE_OF',
    VersionOf: 'VERSION_OF',
    Impacts: 'IMPACTS'
} as const;
export type SemanticLinkRelationType = typeof SemanticLinkRelationType[keyof typeof SemanticLinkRelationType];

/**
 *
 * @export
 * @interface SemanticLinkScan
 */
export interface SemanticLinkScan {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkScanScope}
     * @memberof SemanticLinkScan
     */
    scope: SemanticLinkScanScope;
    /**
     *
     * @type {SemanticLinkScanStatus}
     * @memberof SemanticLinkScan
     */
    status: SemanticLinkScanStatus;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    workflow_run_id: string;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    version: number;
    /**
     * Workspace-scoped self URL for this durable Scan projection.
     * @type {string}
     * @memberof SemanticLinkScan
     */
    status_url: string;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    total_count: number;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    processed_count: number;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    candidate_count: number;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    ignored_count: number;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScan
     */
    failed_count: number;
    /**
     *
     * @type {SemanticLinkScanError}
     * @memberof SemanticLinkScan
     */
    last_error: SemanticLinkScanError | null;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScan
     */
    completed_at: string | null;
}


/**
 *
 * @export
 * @interface SemanticLinkScanAcceptance
 */
export interface SemanticLinkScanAcceptance {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScanAcceptance
     */
    scan_id: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScanAcceptance
     */
    workflow_run_id: string;
    /**
     *
     * @type {SemanticLinkScanStatus}
     * @memberof SemanticLinkScanAcceptance
     */
    status: SemanticLinkScanStatus;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkScanAcceptance
     */
    version: number;
    /**
     * Workspace-scoped authoritative Semantic Link Scan projection URL; workflow_run_id remains the runtime binding.
     * @type {string}
     * @memberof SemanticLinkScanAcceptance
     */
    status_url: string;
}


/**
 *
 * @export
 * @interface SemanticLinkScanError
 */
export interface SemanticLinkScanError {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScanError
     */
    stage: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScanError
     */
    code: string;
    /**
     *
     * @type {boolean}
     * @memberof SemanticLinkScanError
     */
    retryable: boolean;
}
/**
 * @type SemanticLinkScanScope
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type SemanticLinkScanScope = SemanticLinkSmartCollectionScanScopeBinding | SemanticLinkTopicScanScope;

/**
 *
 * @export
 * @interface SemanticLinkScanStartRequest
 */
export interface SemanticLinkScanStartRequest {
    /**
     *
     * @type {string}
     * @memberof SemanticLinkScanStartRequest
     */
    workspace_id: string;
    /**
     *
     * @type {SemanticLinkScanStartRequestScope}
     * @memberof SemanticLinkScanStartRequest
     */
    scope: SemanticLinkScanStartRequestScope;
}
/**
 * @type SemanticLinkScanStartRequestScope
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type SemanticLinkScanStartRequestScope = SemanticLinkSmartCollectionScanScope | SemanticLinkTopicScanScope;


/**
 *
 * @export
 */
export const SemanticLinkScanStatus = {
    Pending: 'PENDING',
    Running: 'RUNNING',
    Succeeded: 'SUCCEEDED',
    Failed: 'FAILED',
    Cancelled: 'CANCELLED'
} as const;
export type SemanticLinkScanStatus = typeof SemanticLinkScanStatus[keyof typeof SemanticLinkScanStatus];

/**
 *
 * @export
 * @interface SemanticLinkSmartCollectionScanScope
 */
export interface SemanticLinkSmartCollectionScanScope {
    /**
     *
     * @type {SemanticLinkSmartCollectionScanScopeKindEnum}
     * @memberof SemanticLinkSmartCollectionScanScope
     */
    kind: SemanticLinkSmartCollectionScanScopeKindEnum;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkSmartCollectionScanScope
     */
    collection_id: string;
}


/**
 * @export
 */
export const SemanticLinkSmartCollectionScanScopeKindEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type SemanticLinkSmartCollectionScanScopeKindEnum = typeof SemanticLinkSmartCollectionScanScopeKindEnum[keyof typeof SemanticLinkSmartCollectionScanScopeKindEnum];

/**
 *
 * @export
 * @interface SemanticLinkSmartCollectionScanScopeBinding
 */
export interface SemanticLinkSmartCollectionScanScopeBinding {
    /**
     *
     * @type {SemanticLinkSmartCollectionScanScopeBindingKindEnum}
     * @memberof SemanticLinkSmartCollectionScanScopeBinding
     */
    kind: SemanticLinkSmartCollectionScanScopeBindingKindEnum;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkSmartCollectionScanScopeBinding
     */
    collection_id: string;
    /**
     *
     * @type {number}
     * @memberof SemanticLinkSmartCollectionScanScopeBinding
     */
    collection_version: number;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkSmartCollectionScanScopeBinding
     */
    query_hash: string;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkSmartCollectionScanScopeBinding
     */
    read_model_revision: string;
}


/**
 * @export
 */
export const SemanticLinkSmartCollectionScanScopeBindingKindEnum = {
    SmartCollection: 'SMART_COLLECTION'
} as const;
export type SemanticLinkSmartCollectionScanScopeBindingKindEnum = typeof SemanticLinkSmartCollectionScanScopeBindingKindEnum[keyof typeof SemanticLinkSmartCollectionScanScopeBindingKindEnum];

/**
 *
 * @export
 * @interface SemanticLinkTopicScanScope
 */
export interface SemanticLinkTopicScanScope {
    /**
     *
     * @type {SemanticLinkTopicScanScopeKindEnum}
     * @memberof SemanticLinkTopicScanScope
     */
    kind: SemanticLinkTopicScanScopeKindEnum;
    /**
     *
     * @type {string}
     * @memberof SemanticLinkTopicScanScope
     */
    topic_id: string;
}


/**
 * @export
 */
export const SemanticLinkTopicScanScopeKindEnum = {
    Topic: 'TOPIC'
} as const;
export type SemanticLinkTopicScanScopeKindEnum = typeof SemanticLinkTopicScanScopeKindEnum[keyof typeof SemanticLinkTopicScanScopeKindEnum];

/**
 *
 * @export
 * @interface ServerEventEnvelope
 */
export interface ServerEventEnvelope {
    /**
     *
     * @type {number}
     * @memberof ServerEventEnvelope
     */
    schema_version: number;
    /**
     *
     * @type {string}
     * @memberof ServerEventEnvelope
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventEnvelope
     */
    type: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventEnvelope
     */
    occurred_at: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventEnvelope
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventEnvelope
     */
    resource_ref: string;
    /**
     *
     * @type {number}
     * @memberof ServerEventEnvelope
     */
    resource_version: number;
    /**
     *
     * @type {ServerEventPayloadSummary}
     * @memberof ServerEventEnvelope
     */
    payload_summary: ServerEventPayloadSummary;
}
/**
 *
 * @export
 * @interface ServerEventPayloadSummary
 */
export interface ServerEventPayloadSummary {
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    source_event_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    conversation_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    workflow_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    question_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    answer_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    model_run_id?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    status?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    publication_status?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    result_type?: string;
    /**
     *
     * @type {string}
     * @memberof ServerEventPayloadSummary
     */
    stage?: string;
    /**
     *
     * @type {ServerEventPayloadSummaryScopeKindEnum}
     * @memberof ServerEventPayloadSummary
     */
    scope_kind?: ServerEventPayloadSummaryScopeKindEnum;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    candidate_count?: number;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    selected_count?: number;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    conflict_count?: number;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    degradation_count?: number;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    rewrite_count?: number;
    /**
     *
     * @type {number}
     * @memberof ServerEventPayloadSummary
     */
    citation_count?: number;
}


/**
 * @export
 */
export const ServerEventPayloadSummaryScopeKindEnum = {
    Collection: 'collection',
    WorkspaceAttachments: 'workspace_attachments'
} as const;
export type ServerEventPayloadSummaryScopeKindEnum = typeof ServerEventPayloadSummaryScopeKindEnum[keyof typeof ServerEventPayloadSummaryScopeKindEnum];

/**
 *
 * @export
 * @interface SessionCredential
 */
export interface SessionCredential {
    /**
     *
     * @type {string}
     * @memberof SessionCredential
     */
    session_id: string;
    /**
     * Returned on Session creation or rotation and required on unsafe cookie-authenticated requests.
     * @type {string}
     * @memberof SessionCredential
     */
    readonly csrf_token: string;
    /**
     *
     * @type {string}
     * @memberof SessionCredential
     */
    expires_at: string;
}
/**
 *
 * @export
 * @interface SessionInfo
 */
export interface SessionInfo {
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    user_label: string;
    /**
     *
     * @type {Array<AuthCapability>}
     * @memberof SessionInfo
     */
    scopes: Array<AuthCapability>;
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    last_seen_at: string;
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    expires_at: string;
    /**
     *
     * @type {string}
     * @memberof SessionInfo
     */
    revoked_at?: string;
}
/**
 *
 * @export
 * @interface SourceVersionPage
 */
export interface SourceVersionPage {
    /**
     *
     * @type {Array<SourceVersionSummary>}
     * @memberof SourceVersionPage
     */
    items: Array<SourceVersionSummary>;
    /**
     *
     * @type {string}
     * @memberof SourceVersionPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface SourceVersionSummary
 */
export interface SourceVersionSummary {
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    source_id: string;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    path: string;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    mime_type: string;
    /**
     *
     * @type {number}
     * @memberof SourceVersionSummary
     */
    byte_size: number;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    captured_at: string;
    /**
     *
     * @type {string}
     * @memberof SourceVersionSummary
     */
    content_hash: string;
    /**
     *
     * @type {SourceVersionSummarySecurityStatusEnum}
     * @memberof SourceVersionSummary
     */
    security_status: SourceVersionSummarySecurityStatusEnum;
    /**
     *
     * @type {SourceVersionSummaryIngestionStatusEnum}
     * @memberof SourceVersionSummary
     */
    ingestion_status?: SourceVersionSummaryIngestionStatusEnum;
    /**
     *
     * @type {SourceVersionSummaryWorkflowStatusEnum}
     * @memberof SourceVersionSummary
     */
    workflow_status?: SourceVersionSummaryWorkflowStatusEnum;
    /**
     * Active Index selection. included binds this Source Version; excluded is Source-scoped and therefore applies to every historical version of the excluded Source.
     * @type {SourceVersionSummaryIndexStatusEnum}
     * @memberof SourceVersionSummary
     */
    index_status?: SourceVersionSummaryIndexStatusEnum;
}


/**
 * @export
 */
export const SourceVersionSummarySecurityStatusEnum = {
    Pending: 'pending',
    Passed: 'passed',
    Quarantined: 'quarantined'
} as const;
export type SourceVersionSummarySecurityStatusEnum = typeof SourceVersionSummarySecurityStatusEnum[keyof typeof SourceVersionSummarySecurityStatusEnum];

/**
 * @export
 */
export const SourceVersionSummaryIngestionStatusEnum = {
    Validating: 'validating',
    Parsing: 'parsing',
    Parsed: 'parsed',
    Chunking: 'chunking',
    Chunked: 'chunked',
    ParseFailed: 'parse_failed',
    Cancelled: 'cancelled'
} as const;
export type SourceVersionSummaryIngestionStatusEnum = typeof SourceVersionSummaryIngestionStatusEnum[keyof typeof SourceVersionSummaryIngestionStatusEnum];

/**
 * @export
 */
export const SourceVersionSummaryWorkflowStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type SourceVersionSummaryWorkflowStatusEnum = typeof SourceVersionSummaryWorkflowStatusEnum[keyof typeof SourceVersionSummaryWorkflowStatusEnum];

/**
 * @export
 */
export const SourceVersionSummaryIndexStatusEnum = {
    Included: 'included',
    Excluded: 'excluded'
} as const;
export type SourceVersionSummaryIndexStatusEnum = typeof SourceVersionSummaryIndexStatusEnum[keyof typeof SourceVersionSummaryIndexStatusEnum];

/**
 *
 * @export
 * @interface StageScore
 */
export interface StageScore {
    /**
     *
     * @type {number}
     * @memberof StageScore
     */
    rank: number;
    /**
     *
     * @type {number}
     * @memberof StageScore
     */
    score: number;
}
/**
 *
 * @export
 * @interface StartInterviewRequest
 */
export interface StartInterviewRequest {
    /**
     *
     * @type {string}
     * @memberof StartInterviewRequest
     */
    workspace_id: string;
    /**
     *
     * @type {InterviewConfig}
     * @memberof StartInterviewRequest
     */
    config: InterviewConfig;
}
/**
 *
 * @export
 * @interface StartModelSettingsActivationRequest
 */
export interface StartModelSettingsActivationRequest {
    /**
     * Exact desired revision to activate. The server generates the activation identifier and binds the current state version.
     * @type {number}
     * @memberof StartModelSettingsActivationRequest
     */
    expected_revision: number;
}
/**
 *
 * @export
 * @interface StartWorkflowRequest
 */
export interface StartWorkflowRequest {
    /**
     *
     * @type {string}
     * @memberof StartWorkflowRequest
     */
    definition_key: string;
    /**
     *
     * @type {number}
     * @memberof StartWorkflowRequest
     */
    definition_version: number;
    /**
     * Deprecated compatibility field; must match the server-registered canonical graph.
     * @type {{ [key: string]: JSONValue; }}
     * @memberof StartWorkflowRequest
     * @deprecated
     */
    graph?: { [key: string]: JSONValue; };
    /**
     *
     * @type {JSONValue}
     * @memberof StartWorkflowRequest
     */
    input: JSONValue | null;
    /**
     *
     * @type {string}
     * @memberof StartWorkflowRequest
     * @deprecated
     */
    first_node_key?: string;
    /**
     *
     * @type {string}
     * @memberof StartWorkflowRequest
     * @deprecated
     */
    first_node_type?: string;
}
/**
 *
 * @export
 * @interface SubmitFeedbackRequest
 */
export interface SubmitFeedbackRequest {
    /**
     *
     * @type {string}
     * @memberof SubmitFeedbackRequest
     */
    workspace_id: string;
    /**
     *
     * @type {SubmitFeedbackRequestFeedbackTypeEnum}
     * @memberof SubmitFeedbackRequest
     */
    feedback_type: SubmitFeedbackRequestFeedbackTypeEnum;
    /**
     *
     * @type {string}
     * @memberof SubmitFeedbackRequest
     */
    citation_id?: string | null;
    /**
     *
     * @type {string}
     * @memberof SubmitFeedbackRequest
     */
    comment?: string | null;
}


/**
 * @export
 */
export const SubmitFeedbackRequestFeedbackTypeEnum = {
    Helpful: 'helpful',
    Incorrect: 'incorrect',
    IrrelevantCitation: 'irrelevant_citation',
    BrokenCitation: 'broken_citation',
    MissingSource: 'missing_source'
} as const;
export type SubmitFeedbackRequestFeedbackTypeEnum = typeof SubmitFeedbackRequestFeedbackTypeEnum[keyof typeof SubmitFeedbackRequestFeedbackTypeEnum];

/**
 *
 * @export
 * @interface SubmitInterviewTurnRequest
 */
export interface SubmitInterviewTurnRequest {
    /**
     *
     * @type {string}
     * @memberof SubmitInterviewTurnRequest
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof SubmitInterviewTurnRequest
     */
    question_id: string;
    /**
     *
     * @type {string}
     * @memberof SubmitInterviewTurnRequest
     */
    user_answer?: string;
}
/**
 *
 * @export
 * @interface SubmitQuestionRequest
 */
export interface SubmitQuestionRequest {
    /**
     *
     * @type {string}
     * @memberof SubmitQuestionRequest
     */
    workspace_id: string;
    /**
     * Execution mode. Omission preserves the historical fixed RAG behavior.
     * @type {SubmitQuestionRequestModeEnum}
     * @memberof SubmitQuestionRequest
     */
    mode?: SubmitQuestionRequestModeEnum;
    /**
     * Non-blank UTF-8 text, at most 8192 bytes, with no NUL.
     * @type {string}
     * @memberof SubmitQuestionRequest
     */
    question: string;
    /**
     *
     * @type {QuestionScopeRequest}
     * @memberof SubmitQuestionRequest
     */
    scope?: QuestionScopeRequest;
    /**
     *
     * @type {SubmitQuestionRequestAnswerDepthEnum}
     * @memberof SubmitQuestionRequest
     */
    answer_depth?: SubmitQuestionRequestAnswerDepthEnum;
    /**
     *
     * @type {SubmitQuestionRequestOutputFormatEnum}
     * @memberof SubmitQuestionRequest
     */
    output_format?: SubmitQuestionRequestOutputFormatEnum;
}


/**
 * @export
 */
export const SubmitQuestionRequestModeEnum = {
    Rag: 'rag',
    WorkspaceAnalysis: 'workspace_analysis'
} as const;
export type SubmitQuestionRequestModeEnum = typeof SubmitQuestionRequestModeEnum[keyof typeof SubmitQuestionRequestModeEnum];

/**
 * @export
 */
export const SubmitQuestionRequestAnswerDepthEnum = {
    Concise: 'concise',
    Standard: 'standard',
    Detailed: 'detailed'
} as const;
export type SubmitQuestionRequestAnswerDepthEnum = typeof SubmitQuestionRequestAnswerDepthEnum[keyof typeof SubmitQuestionRequestAnswerDepthEnum];

/**
 * @export
 */
export const SubmitQuestionRequestOutputFormatEnum = {
    Markdown: 'markdown',
    Outline: 'outline'
} as const;
export type SubmitQuestionRequestOutputFormatEnum = typeof SubmitQuestionRequestOutputFormatEnum[keyof typeof SubmitQuestionRequestOutputFormatEnum];

/**
 * @type SubscribeAnswerDraft200Response
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type SubscribeAnswerDraft200Response = AnswerDraftChunk | AnswerDraftEnd | AnswerDraftReset;

/**
 * @type SuccessfulChatConnectionTest
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type SuccessfulChatConnectionTest = SuccessfulChatConnectionTestOneOf | SuccessfulChatConnectionTestOneOf1;

/**
 *
 * @export
 * @interface SuccessfulChatConnectionTestOneOf
 */
export interface SuccessfulChatConnectionTestOneOf {
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOfProviderEnum}
     * @memberof SuccessfulChatConnectionTestOneOf
     */
    provider?: SuccessfulChatConnectionTestOneOfProviderEnum;
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOfApiStyleEnum}
     * @memberof SuccessfulChatConnectionTestOneOf
     */
    api_style?: SuccessfulChatConnectionTestOneOfApiStyleEnum;
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOfEndpointPathEnum}
     * @memberof SuccessfulChatConnectionTestOneOf
     */
    endpoint_path?: SuccessfulChatConnectionTestOneOfEndpointPathEnum;
}


/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOfProviderEnum = {
    OpenaiCompatible: 'openai-compatible',
    Ollama: 'ollama'
} as const;
export type SuccessfulChatConnectionTestOneOfProviderEnum = typeof SuccessfulChatConnectionTestOneOfProviderEnum[keyof typeof SuccessfulChatConnectionTestOneOfProviderEnum];

/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOfApiStyleEnum = {
    ChatCompletions: 'chat_completions'
} as const;
export type SuccessfulChatConnectionTestOneOfApiStyleEnum = typeof SuccessfulChatConnectionTestOneOfApiStyleEnum[keyof typeof SuccessfulChatConnectionTestOneOfApiStyleEnum];

/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOfEndpointPathEnum = {
    V1ChatCompletions: '/v1/chat/completions'
} as const;
export type SuccessfulChatConnectionTestOneOfEndpointPathEnum = typeof SuccessfulChatConnectionTestOneOfEndpointPathEnum[keyof typeof SuccessfulChatConnectionTestOneOfEndpointPathEnum];

/**
 *
 * @export
 * @interface SuccessfulChatConnectionTestOneOf1
 */
export interface SuccessfulChatConnectionTestOneOf1 {
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOf1ProviderEnum}
     * @memberof SuccessfulChatConnectionTestOneOf1
     */
    provider?: SuccessfulChatConnectionTestOneOf1ProviderEnum;
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOf1ApiStyleEnum}
     * @memberof SuccessfulChatConnectionTestOneOf1
     */
    api_style?: SuccessfulChatConnectionTestOneOf1ApiStyleEnum;
    /**
     *
     * @type {SuccessfulChatConnectionTestOneOf1EndpointPathEnum}
     * @memberof SuccessfulChatConnectionTestOneOf1
     */
    endpoint_path?: SuccessfulChatConnectionTestOneOf1EndpointPathEnum;
}


/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOf1ProviderEnum = {
    OpenaiCompatible: 'openai-compatible'
} as const;
export type SuccessfulChatConnectionTestOneOf1ProviderEnum = typeof SuccessfulChatConnectionTestOneOf1ProviderEnum[keyof typeof SuccessfulChatConnectionTestOneOf1ProviderEnum];

/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOf1ApiStyleEnum = {
    Responses: 'responses'
} as const;
export type SuccessfulChatConnectionTestOneOf1ApiStyleEnum = typeof SuccessfulChatConnectionTestOneOf1ApiStyleEnum[keyof typeof SuccessfulChatConnectionTestOneOf1ApiStyleEnum];

/**
 * @export
 */
export const SuccessfulChatConnectionTestOneOf1EndpointPathEnum = {
    V1Responses: '/v1/responses'
} as const;
export type SuccessfulChatConnectionTestOneOf1EndpointPathEnum = typeof SuccessfulChatConnectionTestOneOf1EndpointPathEnum[keyof typeof SuccessfulChatConnectionTestOneOf1EndpointPathEnum];

/**
 *
 * @export
 * @interface SuccessfulEmbeddingConnectionTest
 */
export interface SuccessfulEmbeddingConnectionTest {
    /**
     *
     * @type {SuccessfulEmbeddingConnectionTestTargetEnum}
     * @memberof SuccessfulEmbeddingConnectionTest
     */
    target?: SuccessfulEmbeddingConnectionTestTargetEnum;
    /**
     *
     * @type {SuccessfulEmbeddingConnectionTestProviderEnum}
     * @memberof SuccessfulEmbeddingConnectionTest
     */
    provider?: SuccessfulEmbeddingConnectionTestProviderEnum;
    /**
     *
     * @type {SuccessfulEmbeddingConnectionTestEndpointPathEnum}
     * @memberof SuccessfulEmbeddingConnectionTest
     */
    endpoint_path?: SuccessfulEmbeddingConnectionTestEndpointPathEnum;
}


/**
 * @export
 */
export const SuccessfulEmbeddingConnectionTestTargetEnum = {
    Embedding: 'embedding'
} as const;
export type SuccessfulEmbeddingConnectionTestTargetEnum = typeof SuccessfulEmbeddingConnectionTestTargetEnum[keyof typeof SuccessfulEmbeddingConnectionTestTargetEnum];

/**
 * @export
 */
export const SuccessfulEmbeddingConnectionTestProviderEnum = {
    OpenaiCompatible: 'openai-compatible',
    Ollama: 'ollama'
} as const;
export type SuccessfulEmbeddingConnectionTestProviderEnum = typeof SuccessfulEmbeddingConnectionTestProviderEnum[keyof typeof SuccessfulEmbeddingConnectionTestProviderEnum];

/**
 * @export
 */
export const SuccessfulEmbeddingConnectionTestEndpointPathEnum = {
    V1Embeddings: '/v1/embeddings'
} as const;
export type SuccessfulEmbeddingConnectionTestEndpointPathEnum = typeof SuccessfulEmbeddingConnectionTestEndpointPathEnum[keyof typeof SuccessfulEmbeddingConnectionTestEndpointPathEnum];

/**
 *
 * @export
 * @interface SystemStatus
 */
export interface SystemStatus {
    /**
     *
     * @type {SystemStatusStatusEnum}
     * @memberof SystemStatus
     */
    status: SystemStatusStatusEnum;
    /**
     *
     * @type {string}
     * @memberof SystemStatus
     */
    version: string;
    /**
     *
     * @type {DatabaseStatus}
     * @memberof SystemStatus
     */
    database: DatabaseStatus;
    /**
     *
     * @type {GraphCapabilityStatus}
     * @memberof SystemStatus
     */
    graph: GraphCapabilityStatus;
    /**
     *
     * @type {SemanticLinkCapabilityStatus}
     * @memberof SystemStatus
     */
    semantic_links: SemanticLinkCapabilityStatus;
    /**
     *
     * @type {RAGCapabilityStatus}
     * @memberof SystemStatus
     */
    rag: RAGCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    collections: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    knowledge_health: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    knowledge_timeline: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    review: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    memory: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    interview: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    authoring: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    capture: OptionalCapabilityStatus;
    /**
     *
     * @type {OptionalCapabilityStatus}
     * @memberof SystemStatus
     */
    organizing: OptionalCapabilityStatus;
    /**
     *
     * @type {AuthCapabilityStatus}
     * @memberof SystemStatus
     */
    auth: AuthCapabilityStatus;
    /**
     *
     * @type {string}
     * @memberof SystemStatus
     */
    request_id: string;
}


/**
 * @export
 */
export const SystemStatusStatusEnum = {
    Ready: 'ready',
    Degraded: 'degraded'
} as const;
export type SystemStatusStatusEnum = typeof SystemStatusStatusEnum[keyof typeof SystemStatusStatusEnum];

/**
 *
 * @export
 * @interface TestGitRemoteConfigRequest
 */
export interface TestGitRemoteConfigRequest {
    /**
     *
     * @type {number}
     * @memberof TestGitRemoteConfigRequest
     */
    expected_revision: number;
    /**
     * Remote input normalized by the server; after trimming it must be a public HTTPS repository URL without userinfo, query, or fragment.
     * @type {string}
     * @memberof TestGitRemoteConfigRequest
     */
    remote_url: string;
    /**
     *
     * @type {string}
     * @memberof TestGitRemoteConfigRequest
     */
    branch: string;
    /**
     *
     * @type {GitSyncTestTokenAction}
     * @memberof TestGitRemoteConfigRequest
     */
    token: GitSyncTestTokenAction;
}
/**
 * @type TestModelSettingsRequest
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type TestModelSettingsRequest = ChatConnectionTest | EmbeddingConnectionTest;

/**
 *
 * @export
 * @interface TextCapture
 */
export interface TextCapture {
    /**
     *
     * @type {TextCaptureKindEnum}
     * @memberof TextCapture
     */
    kind: TextCaptureKindEnum;
    /**
     *
     * @type {string}
     * @memberof TextCapture
     */
    display_name?: string;
    /**
     * UTF-8 plain text. The bounded JSON request must contain no NUL byte.
     * @type {string}
     * @memberof TextCapture
     */
    text: string;
}


/**
 * @export
 */
export const TextCaptureKindEnum = {
    Text: 'TEXT'
} as const;
export type TextCaptureKindEnum = typeof TextCaptureKindEnum[keyof typeof TextCaptureKindEnum];


/**
 *
 * @export
 */
export const TimelineAggregateType = {
    Proposal: 'PROPOSAL',
    Approval: 'APPROVAL',
    GitCommit: 'GIT_COMMIT',
    Topic: 'TOPIC',
    Claim: 'CLAIM',
    Relation: 'RELATION',
    Conflict: 'CONFLICT',
    Document: 'DOCUMENT',
    ArticleRevision: 'ARTICLE_REVISION',
    HealthIssue: 'HEALTH_ISSUE',
    ImpactReport: 'IMPACT_REPORT',
    Artifact: 'ARTIFACT',
    ReviewCard: 'REVIEW_CARD'
} as const;
export type TimelineAggregateType = typeof TimelineAggregateType[keyof typeof TimelineAggregateType];

/**
 *
 * @export
 * @interface Turn
 */
export interface Turn {
    /**
     *
     * @type {Question}
     * @memberof Turn
     */
    question: Question;
    /**
     *
     * @type {Answer}
     * @memberof Turn
     */
    answer: Answer;
}
/**
 *
 * @export
 * @interface TurnPage
 */
export interface TurnPage {
    /**
     *
     * @type {Array<Turn>}
     * @memberof TurnPage
     */
    items: Array<Turn>;
    /**
     * Opaque versioned base64url cursor bound to the resource and sort order.
     * @type {string}
     * @memberof TurnPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface URLCapture
 */
export interface URLCapture {
    /**
     *
     * @type {URLCaptureKindEnum}
     * @memberof URLCapture
     */
    kind: URLCaptureKindEnum;
    /**
     *
     * @type {string}
     * @memberof URLCapture
     */
    display_name?: string;
    /**
     * Canonical public HTTP or HTTPS URL retained as immutable provenance; fetching remains asynchronous and fail-closed.
     * @type {string}
     * @memberof URLCapture
     */
    url: string;
}


/**
 * @export
 */
export const URLCaptureKindEnum = {
    Url: 'URL'
} as const;
export type URLCaptureKindEnum = typeof URLCaptureKindEnum[keyof typeof URLCaptureKindEnum];

/**
 *
 * @export
 * @interface UpdateLearningPathStatusRequest
 */
export interface UpdateLearningPathStatusRequest {
    /**
     *
     * @type {string}
     * @memberof UpdateLearningPathStatusRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof UpdateLearningPathStatusRequest
     */
    expected_version: number;
    /**
     *
     * @type {UpdateLearningPathStatusRequestStatusEnum}
     * @memberof UpdateLearningPathStatusRequest
     */
    status: UpdateLearningPathStatusRequestStatusEnum;
}


/**
 * @export
 */
export const UpdateLearningPathStatusRequestStatusEnum = {
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Completed: 'COMPLETED'
} as const;
export type UpdateLearningPathStatusRequestStatusEnum = typeof UpdateLearningPathStatusRequestStatusEnum[keyof typeof UpdateLearningPathStatusRequestStatusEnum];

/**
 *
 * @export
 * @interface UpdateLearningPathStepRequest
 */
export interface UpdateLearningPathStepRequest {
    /**
     *
     * @type {string}
     * @memberof UpdateLearningPathStepRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof UpdateLearningPathStepRequest
     */
    expected_version: number;
    /**
     *
     * @type {UpdateLearningPathStepRequestStatusEnum}
     * @memberof UpdateLearningPathStepRequest
     */
    status: UpdateLearningPathStepRequestStatusEnum;
}


/**
 * @export
 */
export const UpdateLearningPathStepRequestStatusEnum = {
    InProgress: 'IN_PROGRESS',
    Completed: 'COMPLETED',
    Skipped: 'SKIPPED'
} as const;
export type UpdateLearningPathStepRequestStatusEnum = typeof UpdateLearningPathStepRequestStatusEnum[keyof typeof UpdateLearningPathStepRequestStatusEnum];

/**
 *
 * @export
 * @interface UpdateModelSettingsRequest
 */
export interface UpdateModelSettingsRequest {
    /**
     * Latest desired revision observed by the caller. A mismatch or active rollout returns 409 without echoing settings or credentials.
     * @type {number}
     * @memberof UpdateModelSettingsRequest
     */
    expected_revision: number;
    /**
     *
     * @type {ModelChatSettingsDraft}
     * @memberof UpdateModelSettingsRequest
     */
    chat: ModelChatSettingsDraft;
    /**
     *
     * @type {ModelEmbeddingSettingsDraft}
     * @memberof UpdateModelSettingsRequest
     */
    embedding: ModelEmbeddingSettingsDraft;
}
/**
 *
 * @export
 * @interface UpdateReviewLearningPathStatusRequest
 */
export interface UpdateReviewLearningPathStatusRequest {
    /**
     *
     * @type {string}
     * @memberof UpdateReviewLearningPathStatusRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof UpdateReviewLearningPathStatusRequest
     */
    expected_version: number;
    /**
     *
     * @type {UpdateReviewLearningPathStatusRequestStatusEnum}
     * @memberof UpdateReviewLearningPathStatusRequest
     */
    status: UpdateReviewLearningPathStatusRequestStatusEnum;
}


/**
 * @export
 */
export const UpdateReviewLearningPathStatusRequestStatusEnum = {
    Active: 'ACTIVE',
    Paused: 'PAUSED',
    Completed: 'COMPLETED'
} as const;
export type UpdateReviewLearningPathStatusRequestStatusEnum = typeof UpdateReviewLearningPathStatusRequestStatusEnum[keyof typeof UpdateReviewLearningPathStatusRequestStatusEnum];

/**
 *
 * @export
 * @interface UpdateReviewLearningPathStepRequest
 */
export interface UpdateReviewLearningPathStepRequest {
    /**
     *
     * @type {string}
     * @memberof UpdateReviewLearningPathStepRequest
     */
    workspace_id: string;
    /**
     *
     * @type {number}
     * @memberof UpdateReviewLearningPathStepRequest
     */
    expected_version: number;
    /**
     *
     * @type {UpdateReviewLearningPathStepRequestStatusEnum}
     * @memberof UpdateReviewLearningPathStepRequest
     */
    status: UpdateReviewLearningPathStepRequestStatusEnum;
}


/**
 * @export
 */
export const UpdateReviewLearningPathStepRequestStatusEnum = {
    InProgress: 'IN_PROGRESS',
    Completed: 'COMPLETED',
    Skipped: 'SKIPPED'
} as const;
export type UpdateReviewLearningPathStepRequestStatusEnum = typeof UpdateReviewLearningPathStepRequestStatusEnum[keyof typeof UpdateReviewLearningPathStepRequestStatusEnum];

/**
 *
 * @export
 * @interface UpdateWorkingDraftRequest
 */
export interface UpdateWorkingDraftRequest {
    /**
     *
     * @type {number}
     * @memberof UpdateWorkingDraftRequest
     */
    expected_version: number;
    /**
     *
     * @type {string}
     * @memberof UpdateWorkingDraftRequest
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof UpdateWorkingDraftRequest
     */
    target_path: string;
    /**
     *
     * @type {string}
     * @memberof UpdateWorkingDraftRequest
     */
    body: string;
}
/**
 * The raw distance produced by the Active Embedding Version's persisted pgvector distance operator; lower ranks first and values are not comparable across models.
 * @export
 * @interface VectorDistance
 */
export interface VectorDistance {
    /**
     *
     * @type {number}
     * @memberof VectorDistance
     */
    rank: number;
    /**
     *
     * @type {number}
     * @memberof VectorDistance
     */
    distance: number;
}
/**
 *
 * @export
 * @interface WorkflowControlRequest
 */
export interface WorkflowControlRequest {
    /**
     *
     * @type {number}
     * @memberof WorkflowControlRequest
     */
    expected_version: number;
}
/**
 *
 * @export
 * @interface WorkflowControlResult
 */
export interface WorkflowControlResult {
    /**
     *
     * @type {string}
     * @memberof WorkflowControlResult
     */
    workflow_run_id: string;
    /**
     *
     * @type {WorkflowControlResultStatusEnum}
     * @memberof WorkflowControlResult
     */
    status: WorkflowControlResultStatusEnum;
    /**
     *
     * @type {number}
     * @memberof WorkflowControlResult
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowControlResult
     */
    status_url: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowControlResult
     */
    pause_requested: boolean;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowControlResult
     */
    cancel_requested: boolean;
}


/**
 * @export
 */
export const WorkflowControlResultStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type WorkflowControlResultStatusEnum = typeof WorkflowControlResultStatusEnum[keyof typeof WorkflowControlResultStatusEnum];

/**
 * @type WorkflowHumanTaskReview
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkflowHumanTaskReview = { kind: 'MERGE_COMPARISON' } & WorkflowMergeComparisonReview | { kind: 'TOPIC_OUTLINE' } & WorkflowTopicOutlineReview;

/**
 *
 * @export
 * @interface WorkflowMergeCategory
 */
export interface WorkflowMergeCategory {
    /**
     *
     * @type {WorkflowMergeCategoryCategoryEnum}
     * @memberof WorkflowMergeCategory
     */
    category: WorkflowMergeCategoryCategoryEnum;
    /**
     *
     * @type {number}
     * @memberof WorkflowMergeCategory
     */
    count: number;
}


/**
 * @export
 */
export const WorkflowMergeCategoryCategoryEnum = {
    Duplicate: 'DUPLICATE',
    Complementary: 'COMPLEMENTARY',
    Conflict: 'CONFLICT',
    Unique: 'UNIQUE'
} as const;
export type WorkflowMergeCategoryCategoryEnum = typeof WorkflowMergeCategoryCategoryEnum[keyof typeof WorkflowMergeCategoryCategoryEnum];

/**
 *
 * @export
 * @interface WorkflowMergeComparisonReview
 */
export interface WorkflowMergeComparisonReview {
    /**
     *
     * @type {WorkflowMergeComparisonReviewKindEnum}
     * @memberof WorkflowMergeComparisonReview
     */
    kind: WorkflowMergeComparisonReviewKindEnum;
    /**
     *
     * @type {WorkflowMergeComparisonReviewSchemaVersionEnum}
     * @memberof WorkflowMergeComparisonReview
     */
    schema_version: WorkflowMergeComparisonReviewSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    run_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    task_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    node_run_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    snapshot_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    snapshot_hash: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    artifact_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    revision_hash: string;
    /**
     * Frozen template-derived default. Clients may replace it only with another canonical Workspace-relative Markdown path at approval time.
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    default_target_path: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    diff_hash: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeComparisonReview
     */
    diff_preview: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowMergeComparisonReview
     */
    diff_truncated: boolean;
    /**
     *
     * @type {number}
     * @memberof WorkflowMergeComparisonReview
     */
    conflict_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkflowMergeComparisonReview
     */
    evidence_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkflowMergeComparisonReview
     */
    document_count: number;
    /**
     *
     * @type {Array<JSONValue>}
     * @memberof WorkflowMergeComparisonReview
     */
    categories: Array<JSONValue>;
    /**
     *
     * @type {Array<WorkflowMergeEvidence>}
     * @memberof WorkflowMergeComparisonReview
     */
    comparison: Array<WorkflowMergeEvidence>;
}


/**
 * @export
 */
export const WorkflowMergeComparisonReviewKindEnum = {
    MergeComparison: 'MERGE_COMPARISON'
} as const;
export type WorkflowMergeComparisonReviewKindEnum = typeof WorkflowMergeComparisonReviewKindEnum[keyof typeof WorkflowMergeComparisonReviewKindEnum];

/**
 * @export
 */
export const WorkflowMergeComparisonReviewSchemaVersionEnum = {
    NUMBER_1: 1
} as const;
export type WorkflowMergeComparisonReviewSchemaVersionEnum = typeof WorkflowMergeComparisonReviewSchemaVersionEnum[keyof typeof WorkflowMergeComparisonReviewSchemaVersionEnum];

/**
 *
 * @export
 * @interface WorkflowMergeDocumentEvidence
 */
export interface WorkflowMergeDocumentEvidence {
    /**
     *
     * @type {WorkflowMergeDocumentEvidenceCategoryEnum}
     * @memberof WorkflowMergeDocumentEvidence
     */
    category: WorkflowMergeDocumentEvidenceCategoryEnum;
    /**
     *
     * @type {WorkflowMergeDocumentEvidenceKindEnum}
     * @memberof WorkflowMergeDocumentEvidence
     */
    kind: WorkflowMergeDocumentEvidenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeDocumentEvidence
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeDocumentEvidence
     */
    article_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof WorkflowMergeDocumentEvidence
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeDocumentEvidence
     */
    content_hash: string;
}


/**
 * @export
 */
export const WorkflowMergeDocumentEvidenceCategoryEnum = {
    Duplicate: 'DUPLICATE',
    Complementary: 'COMPLEMENTARY',
    Conflict: 'CONFLICT',
    Unique: 'UNIQUE'
} as const;
export type WorkflowMergeDocumentEvidenceCategoryEnum = typeof WorkflowMergeDocumentEvidenceCategoryEnum[keyof typeof WorkflowMergeDocumentEvidenceCategoryEnum];

/**
 * @export
 */
export const WorkflowMergeDocumentEvidenceKindEnum = {
    DocumentRevision: 'DOCUMENT_REVISION'
} as const;
export type WorkflowMergeDocumentEvidenceKindEnum = typeof WorkflowMergeDocumentEvidenceKindEnum[keyof typeof WorkflowMergeDocumentEvidenceKindEnum];

/**
 * @type WorkflowMergeEvidence
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkflowMergeEvidence = { kind: 'DOCUMENT_REVISION' } & WorkflowMergeDocumentEvidence | { kind: 'SOURCE_VERSION' } & WorkflowMergeSourceEvidence;

/**
 *
 * @export
 * @interface WorkflowMergeSourceEvidence
 */
export interface WorkflowMergeSourceEvidence {
    /**
     *
     * @type {WorkflowMergeSourceEvidenceCategoryEnum}
     * @memberof WorkflowMergeSourceEvidence
     */
    category: WorkflowMergeSourceEvidenceCategoryEnum;
    /**
     *
     * @type {WorkflowMergeSourceEvidenceKindEnum}
     * @memberof WorkflowMergeSourceEvidence
     */
    kind: WorkflowMergeSourceEvidenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeSourceEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeSourceEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeSourceEvidence
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowMergeSourceEvidence
     */
    excerpt_hash: string;
}


/**
 * @export
 */
export const WorkflowMergeSourceEvidenceCategoryEnum = {
    Duplicate: 'DUPLICATE',
    Complementary: 'COMPLEMENTARY',
    Conflict: 'CONFLICT',
    Unique: 'UNIQUE'
} as const;
export type WorkflowMergeSourceEvidenceCategoryEnum = typeof WorkflowMergeSourceEvidenceCategoryEnum[keyof typeof WorkflowMergeSourceEvidenceCategoryEnum];

/**
 * @export
 */
export const WorkflowMergeSourceEvidenceKindEnum = {
    SourceVersion: 'SOURCE_VERSION'
} as const;
export type WorkflowMergeSourceEvidenceKindEnum = typeof WorkflowMergeSourceEvidenceKindEnum[keyof typeof WorkflowMergeSourceEvidenceKindEnum];

/**
 *
 * @export
 * @interface WorkflowProjection
 */
export interface WorkflowProjection {
    /**
     *
     * @type {string}
     * @memberof WorkflowProjection
     */
    run_id: string;
    /**
     *
     * @type {WorkflowProjectionStatusEnum}
     * @memberof WorkflowProjection
     */
    status: WorkflowProjectionStatusEnum;
    /**
     *
     * @type {number}
     * @memberof WorkflowProjection
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowProjection
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowProjection
     */
    status_url: string;
}


/**
 * @export
 */
export const WorkflowProjectionStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    Paused: 'paused',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type WorkflowProjectionStatusEnum = typeof WorkflowProjectionStatusEnum[keyof typeof WorkflowProjectionStatusEnum];

/**
 *
 * @export
 * @interface WorkflowReviewDocumentEvidence
 */
export interface WorkflowReviewDocumentEvidence {
    /**
     *
     * @type {WorkflowReviewDocumentEvidenceKindEnum}
     * @memberof WorkflowReviewDocumentEvidence
     */
    kind: WorkflowReviewDocumentEvidenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewDocumentEvidence
     */
    document_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewDocumentEvidence
     */
    article_revision_id: string;
    /**
     *
     * @type {number}
     * @memberof WorkflowReviewDocumentEvidence
     */
    revision_no: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewDocumentEvidence
     */
    content_hash: string;
}


/**
 * @export
 */
export const WorkflowReviewDocumentEvidenceKindEnum = {
    DocumentRevision: 'DOCUMENT_REVISION'
} as const;
export type WorkflowReviewDocumentEvidenceKindEnum = typeof WorkflowReviewDocumentEvidenceKindEnum[keyof typeof WorkflowReviewDocumentEvidenceKindEnum];

/**
 * @type WorkflowReviewEvidence
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkflowReviewEvidence = { kind: 'DOCUMENT_REVISION' } & WorkflowReviewDocumentEvidence | { kind: 'SOURCE_VERSION' } & WorkflowReviewSourceEvidence;

/**
 *
 * @export
 * @interface WorkflowReviewSourceEvidence
 */
export interface WorkflowReviewSourceEvidence {
    /**
     *
     * @type {WorkflowReviewSourceEvidenceKindEnum}
     * @memberof WorkflowReviewSourceEvidence
     */
    kind: WorkflowReviewSourceEvidenceKindEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewSourceEvidence
     */
    source_version_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewSourceEvidence
     */
    source_span_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewSourceEvidence
     */
    content_hash: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowReviewSourceEvidence
     */
    excerpt_hash: string;
}


/**
 * @export
 */
export const WorkflowReviewSourceEvidenceKindEnum = {
    SourceVersion: 'SOURCE_VERSION'
} as const;
export type WorkflowReviewSourceEvidenceKindEnum = typeof WorkflowReviewSourceEvidenceKindEnum[keyof typeof WorkflowReviewSourceEvidenceKindEnum];

/**
 *
 * @export
 * @interface WorkflowRun
 */
export interface WorkflowRun {
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    definition_id: string;
    /**
     *
     * @type {WorkflowRunStatusEnum}
     * @memberof WorkflowRun
     */
    status: WorkflowRunStatusEnum;
    /**
     *
     * @type {JSONValue}
     * @memberof WorkflowRun
     */
    input: JSONValue | null;
    /**
     *
     * @type {JSONValue}
     * @memberof WorkflowRun
     */
    output?: JSONValue | null;
    /**
     *
     * @type {number}
     * @memberof WorkflowRun
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRun
     */
    completed_at?: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowRun
     */
    pause_requested: boolean;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowRun
     */
    cancel_requested: boolean;
    /**
     *
     * @type {PendingWorkflowHumanTask}
     * @memberof WorkflowRun
     */
    human_task: PendingWorkflowHumanTask | null;
}


/**
 * @export
 */
export const WorkflowRunStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type WorkflowRunStatusEnum = typeof WorkflowRunStatusEnum[keyof typeof WorkflowRunStatusEnum];

/**
 *
 * @export
 * @interface WorkflowRunPage
 */
export interface WorkflowRunPage {
    /**
     *
     * @type {Array<WorkflowRunSummary>}
     * @memberof WorkflowRunPage
     */
    items: Array<WorkflowRunSummary>;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface WorkflowRunSummary
 */
export interface WorkflowRunSummary {
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    definition_key: string;
    /**
     *
     * @type {number}
     * @memberof WorkflowRunSummary
     */
    definition_version: number;
    /**
     *
     * @type {WorkflowRunSummaryStatusEnum}
     * @memberof WorkflowRunSummary
     */
    status: WorkflowRunSummaryStatusEnum;
    /**
     *
     * @type {number}
     * @memberof WorkflowRunSummary
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    updated_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowRunSummary
     */
    completed_at?: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowRunSummary
     */
    waiting_for_human: boolean;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowRunSummary
     */
    pause_requested: boolean;
    /**
     *
     * @type {boolean}
     * @memberof WorkflowRunSummary
     */
    cancel_requested: boolean;
}


/**
 * @export
 */
export const WorkflowRunSummaryStatusEnum = {
    Pending: 'pending',
    Running: 'running',
    WaitingForHuman: 'waiting_for_human',
    RetryWait: 'retry_wait',
    Paused: 'paused',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type WorkflowRunSummaryStatusEnum = typeof WorkflowRunSummaryStatusEnum[keyof typeof WorkflowRunSummaryStatusEnum];

/**
 *
 * @export
 * @interface WorkflowStart
 */
export interface WorkflowStart {
    /**
     *
     * @type {string}
     * @memberof WorkflowStart
     */
    workflow_run_id: string;
    /**
     *
     * @type {WorkflowStartStatusEnum}
     * @memberof WorkflowStart
     */
    status: WorkflowStartStatusEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowStart
     */
    status_url: string;
}


/**
 * @export
 */
export const WorkflowStartStatusEnum = {
    Pending: 'pending'
} as const;
export type WorkflowStartStatusEnum = typeof WorkflowStartStatusEnum[keyof typeof WorkflowStartStatusEnum];

/**
 *
 * @export
 * @interface WorkflowTopicOutlineReview
 */
export interface WorkflowTopicOutlineReview {
    /**
     *
     * @type {WorkflowTopicOutlineReviewKindEnum}
     * @memberof WorkflowTopicOutlineReview
     */
    kind: WorkflowTopicOutlineReviewKindEnum;
    /**
     *
     * @type {WorkflowTopicOutlineReviewSchemaVersionEnum}
     * @memberof WorkflowTopicOutlineReview
     */
    schema_version: WorkflowTopicOutlineReviewSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    run_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    task_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    node_run_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    snapshot_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    snapshot_hash: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    template_revision_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineReview
     */
    template_hash: string;
    /**
     *
     * @type {Array<WorkflowTopicOutlineSection>}
     * @memberof WorkflowTopicOutlineReview
     */
    outline: Array<WorkflowTopicOutlineSection>;
}


/**
 * @export
 */
export const WorkflowTopicOutlineReviewKindEnum = {
    TopicOutline: 'TOPIC_OUTLINE'
} as const;
export type WorkflowTopicOutlineReviewKindEnum = typeof WorkflowTopicOutlineReviewKindEnum[keyof typeof WorkflowTopicOutlineReviewKindEnum];

/**
 * @export
 */
export const WorkflowTopicOutlineReviewSchemaVersionEnum = {
    NUMBER_1: 1
} as const;
export type WorkflowTopicOutlineReviewSchemaVersionEnum = typeof WorkflowTopicOutlineReviewSchemaVersionEnum[keyof typeof WorkflowTopicOutlineReviewSchemaVersionEnum];

/**
 * @type WorkflowTopicOutlineSection
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkflowTopicOutlineSection = WorkflowTopicOutlineSectionGap | WorkflowTopicOutlineSectionSupported;

/**
 *
 * @export
 * @interface WorkflowTopicOutlineSectionGap
 */
export interface WorkflowTopicOutlineSectionGap {
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineSectionGap
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineSectionGap
     */
    title: string;
    /**
     *
     * @type {Array<WorkflowReviewEvidence>}
     * @memberof WorkflowTopicOutlineSectionGap
     */
    supports: Array<WorkflowReviewEvidence>;
    /**
     *
     * @type {WorkflowTopicOutlineSectionGapGapCodeEnum}
     * @memberof WorkflowTopicOutlineSectionGap
     */
    gap_code: WorkflowTopicOutlineSectionGapGapCodeEnum;
}


/**
 * @export
 */
export const WorkflowTopicOutlineSectionGapGapCodeEnum = {
    EvidenceUnavailable: 'EVIDENCE_UNAVAILABLE'
} as const;
export type WorkflowTopicOutlineSectionGapGapCodeEnum = typeof WorkflowTopicOutlineSectionGapGapCodeEnum[keyof typeof WorkflowTopicOutlineSectionGapGapCodeEnum];

/**
 *
 * @export
 * @interface WorkflowTopicOutlineSectionSupported
 */
export interface WorkflowTopicOutlineSectionSupported {
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineSectionSupported
     */
    key: string;
    /**
     *
     * @type {string}
     * @memberof WorkflowTopicOutlineSectionSupported
     */
    title: string;
    /**
     *
     * @type {Array<WorkflowReviewEvidence>}
     * @memberof WorkflowTopicOutlineSectionSupported
     */
    supports: Array<WorkflowReviewEvidence>;
    /**
     *
     * @type {null}
     * @memberof WorkflowTopicOutlineSectionSupported
     */
    gap_code: null;
}
/**
 *
 * @export
 * @interface WorkingDraft
 */
export interface WorkingDraft {
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    document_id: string | null;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    target_path: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    body: string;
    /**
     *
     * @type {WorkingDraftStatusEnum}
     * @memberof WorkingDraft
     */
    status: WorkingDraftStatusEnum;
    /**
     *
     * @type {number}
     * @memberof WorkingDraft
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraft
     */
    updated_at: string;
}


/**
 * @export
 */
export const WorkingDraftStatusEnum = {
    Editing: 'EDITING',
    Archived: 'ARCHIVED'
} as const;
export type WorkingDraftStatusEnum = typeof WorkingDraftStatusEnum[keyof typeof WorkingDraftStatusEnum];

/**
 *
 * @export
 * @interface WorkingDraftCommandResult
 */
export interface WorkingDraftCommandResult {
    /**
     *
     * @type {WorkingDraft}
     * @memberof WorkingDraftCommandResult
     */
    working_draft: WorkingDraft;
    /**
     *
     * @type {boolean}
     * @memberof WorkingDraftCommandResult
     */
    replayed: boolean;
}
/**
 *
 * @export
 * @interface WorkingDraftPage
 */
export interface WorkingDraftPage {
    /**
     *
     * @type {string}
     * @memberof WorkingDraftPage
     */
    workspace_id: string;
    /**
     *
     * @type {Array<WorkingDraftSummary>}
     * @memberof WorkingDraftPage
     */
    items: Array<WorkingDraftSummary>;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftPage
     */
    next_cursor?: string;
}
/**
 *
 * @export
 * @interface WorkingDraftSummary
 */
export interface WorkingDraftSummary {
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    document_id: string | null;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    title: string;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    target_path: string;
    /**
     *
     * @type {WorkingDraftSummaryStatusEnum}
     * @memberof WorkingDraftSummary
     */
    status: WorkingDraftSummaryStatusEnum;
    /**
     *
     * @type {number}
     * @memberof WorkingDraftSummary
     */
    version: number;
    /**
     *
     * @type {string}
     * @memberof WorkingDraftSummary
     */
    updated_at: string;
}


/**
 * @export
 */
export const WorkingDraftSummaryStatusEnum = {
    Editing: 'EDITING',
    Archived: 'ARCHIVED'
} as const;
export type WorkingDraftSummaryStatusEnum = typeof WorkingDraftSummaryStatusEnum[keyof typeof WorkingDraftSummaryStatusEnum];

/**
 *
 * @export
 * @interface Workspace
 */
export interface Workspace {
    /**
     *
     * @type {string}
     * @memberof Workspace
     */
    id: string;
    /**
     *
     * @type {string}
     * @memberof Workspace
     */
    name: string;
    /**
     *
     * @type {string}
     * @memberof Workspace
     */
    root_path: string;
    /**
     *
     * @type {WorkspaceStatusEnum}
     * @memberof Workspace
     */
    status: WorkspaceStatusEnum;
    /**
     *
     * @type {number}
     * @memberof Workspace
     */
    version: number;
    /**
     *
     * @type {GitStatus}
     * @memberof Workspace
     */
    git: GitStatus;
    /**
     *
     * @type {Array<string>}
     * @memberof Workspace
     */
    warnings?: Array<string>;
    /**
     *
     * @type {string}
     * @memberof Workspace
     */
    created_at: string;
    /**
     *
     * @type {string}
     * @memberof Workspace
     */
    updated_at: string;
}


/**
 * @export
 */
export const WorkspaceStatusEnum = {
    Active: 'active'
} as const;
export type WorkspaceStatusEnum = typeof WorkspaceStatusEnum[keyof typeof WorkspaceStatusEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisAnswerResult
 */
export interface WorkspaceAnalysisAnswerResult {
    /**
     *
     * @type {WorkspaceAnalysisAnswerResultResultTypeEnum}
     * @memberof WorkspaceAnalysisAnswerResult
     */
    result_type: WorkspaceAnalysisAnswerResultResultTypeEnum;
    /**
     *
     * @type {WorkspaceAnalysisAnswerResultSchemaIdEnum}
     * @memberof WorkspaceAnalysisAnswerResult
     */
    schema_id: WorkspaceAnalysisAnswerResultSchemaIdEnum;
    /**
     *
     * @type {WorkspaceAnalysisAnswerResultSchemaVersionEnum}
     * @memberof WorkspaceAnalysisAnswerResult
     */
    schema_version: WorkspaceAnalysisAnswerResultSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisAnswerResult
     */
    model_run_ref: string;
    /**
     *
     * @type {WorkspaceAnalysisAnswerResultPayload}
     * @memberof WorkspaceAnalysisAnswerResult
     */
    payload: WorkspaceAnalysisAnswerResultPayload;
}


/**
 * @export
 */
export const WorkspaceAnalysisAnswerResultResultTypeEnum = {
    WorkspaceAnalysis: 'workspace_analysis'
} as const;
export type WorkspaceAnalysisAnswerResultResultTypeEnum = typeof WorkspaceAnalysisAnswerResultResultTypeEnum[keyof typeof WorkspaceAnalysisAnswerResultResultTypeEnum];

/**
 * @export
 */
export const WorkspaceAnalysisAnswerResultSchemaIdEnum = {
    ConversationWorkspaceAnalysisAnswer: 'conversation.workspace_analysis_answer'
} as const;
export type WorkspaceAnalysisAnswerResultSchemaIdEnum = typeof WorkspaceAnalysisAnswerResultSchemaIdEnum[keyof typeof WorkspaceAnalysisAnswerResultSchemaIdEnum];

/**
 * @export
 */
export const WorkspaceAnalysisAnswerResultSchemaVersionEnum = {
    V1: 'v1'
} as const;
export type WorkspaceAnalysisAnswerResultSchemaVersionEnum = typeof WorkspaceAnalysisAnswerResultSchemaVersionEnum[keyof typeof WorkspaceAnalysisAnswerResultSchemaVersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisAnswerResultPayload
 */
export interface WorkspaceAnalysisAnswerResultPayload {
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    answer_markdown: string;
    /**
     *
     * @type {Array<RAGResultCitation>}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    citations: Array<RAGResultCitation>;
    /**
     *
     * @type {WorkspaceAnalysisGitStatus}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    git_status: WorkspaceAnalysisGitStatus;
    /**
     *
     * @type {WorkspaceAnalysisBudgetSummary}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    budget: WorkspaceAnalysisBudgetSummary;
    /**
     *
     * @type {WorkspaceAnalysisProposalSuggestion}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    proposal_suggestion: WorkspaceAnalysisProposalSuggestion | null;
    /**
     *
     * @type {WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum}
     * @memberof WorkspaceAnalysisAnswerResultPayload
     */
    termination_reason: WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum = {
    Completed: 'COMPLETED'
} as const;
export type WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum = typeof WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum[keyof typeof WorkspaceAnalysisAnswerResultPayloadTerminationReasonEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisBudgetSummary
 */
export interface WorkspaceAnalysisBudgetSummary {
    /**
     *
     * @type {WorkspaceAnalysisBudgetSummaryModelCallsEnum}
     * @memberof WorkspaceAnalysisBudgetSummary
     */
    model_calls: WorkspaceAnalysisBudgetSummaryModelCallsEnum;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisBudgetSummary
     */
    tool_calls: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisBudgetSummary
     */
    input_tokens: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisBudgetSummary
     */
    output_tokens: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisBudgetSummary
     */
    estimated_cost_microunits: number | null;
}


/**
 * @export
 */
export const WorkspaceAnalysisBudgetSummaryModelCallsEnum = {
    NUMBER_3: 3
} as const;
export type WorkspaceAnalysisBudgetSummaryModelCallsEnum = typeof WorkspaceAnalysisBudgetSummaryModelCallsEnum[keyof typeof WorkspaceAnalysisBudgetSummaryModelCallsEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisGitStatus
 */
export interface WorkspaceAnalysisGitStatus {
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisGitStatus
     */
    branch: string;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisGitStatus
     */
    head: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkspaceAnalysisGitStatus
     */
    clean: boolean;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisGitStatus
     */
    staged_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisGitStatus
     */
    unstaged_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisGitStatus
     */
    untracked_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisGitStatus
     */
    conflict_count: number;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisProposalSuggestion
 */
export interface WorkspaceAnalysisProposalSuggestion {
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisProposalSuggestion
     */
    summary: string;
    /**
     *
     * @type {Array<string>}
     * @memberof WorkspaceAnalysisProposalSuggestion
     */
    citation_ids: Array<string>;
    /**
     *
     * @type {WorkspaceAnalysisProposalSuggestionHrefEnum}
     * @memberof WorkspaceAnalysisProposalSuggestion
     */
    href: WorkspaceAnalysisProposalSuggestionHrefEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisProposalSuggestionHrefEnum = {
    Proposals: '/proposals'
} as const;
export type WorkspaceAnalysisProposalSuggestionHrefEnum = typeof WorkspaceAnalysisProposalSuggestionHrefEnum[keyof typeof WorkspaceAnalysisProposalSuggestionHrefEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisRefusalResult
 */
export interface WorkspaceAnalysisRefusalResult {
    /**
     *
     * @type {WorkspaceAnalysisRefusalResultResultTypeEnum}
     * @memberof WorkspaceAnalysisRefusalResult
     */
    result_type: WorkspaceAnalysisRefusalResultResultTypeEnum;
    /**
     *
     * @type {WorkspaceAnalysisRefusalResultSchemaIdEnum}
     * @memberof WorkspaceAnalysisRefusalResult
     */
    schema_id: WorkspaceAnalysisRefusalResultSchemaIdEnum;
    /**
     *
     * @type {WorkspaceAnalysisRefusalResultSchemaVersionEnum}
     * @memberof WorkspaceAnalysisRefusalResult
     */
    schema_version: WorkspaceAnalysisRefusalResultSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisRefusalResult
     */
    model_run_ref: string | null;
    /**
     *
     * @type {WorkspaceAnalysisRefusalResultPayload}
     * @memberof WorkspaceAnalysisRefusalResult
     */
    payload: WorkspaceAnalysisRefusalResultPayload;
}


/**
 * @export
 */
export const WorkspaceAnalysisRefusalResultResultTypeEnum = {
    WorkspaceAnalysisRefusal: 'workspace_analysis_refusal'
} as const;
export type WorkspaceAnalysisRefusalResultResultTypeEnum = typeof WorkspaceAnalysisRefusalResultResultTypeEnum[keyof typeof WorkspaceAnalysisRefusalResultResultTypeEnum];

/**
 * @export
 */
export const WorkspaceAnalysisRefusalResultSchemaIdEnum = {
    ConversationWorkspaceAnalysisRefusal: 'conversation.workspace_analysis_refusal'
} as const;
export type WorkspaceAnalysisRefusalResultSchemaIdEnum = typeof WorkspaceAnalysisRefusalResultSchemaIdEnum[keyof typeof WorkspaceAnalysisRefusalResultSchemaIdEnum];

/**
 * @export
 */
export const WorkspaceAnalysisRefusalResultSchemaVersionEnum = {
    V1: 'v1'
} as const;
export type WorkspaceAnalysisRefusalResultSchemaVersionEnum = typeof WorkspaceAnalysisRefusalResultSchemaVersionEnum[keyof typeof WorkspaceAnalysisRefusalResultSchemaVersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisRefusalResultPayload
 */
export interface WorkspaceAnalysisRefusalResultPayload {
    /**
     *
     * @type {WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum}
     * @memberof WorkspaceAnalysisRefusalResultPayload
     */
    reason_code: WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisRefusalResultPayload
     */
    summary: string;
}


/**
 * @export
 */
export const WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum = {
    WorkspaceAnalysisEvidenceInsufficient: 'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
    WorkspaceAnalysisCitationInvalid: 'WORKSPACE_ANALYSIS_CITATION_INVALID',
    WorkspaceAnalysisFaithfulnessRejected: 'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
    WorkspaceAnalysisModelRefused: 'WORKSPACE_ANALYSIS_MODEL_REFUSED'
} as const;
export type WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum = typeof WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum[keyof typeof WorkspaceAnalysisRefusalResultPayloadReasonCodeEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTerminationResult
 */
export interface WorkspaceAnalysisTerminationResult {
    /**
     *
     * @type {WorkspaceAnalysisTerminationResultResultTypeEnum}
     * @memberof WorkspaceAnalysisTerminationResult
     */
    result_type: WorkspaceAnalysisTerminationResultResultTypeEnum;
    /**
     *
     * @type {WorkspaceAnalysisTerminationResultSchemaIdEnum}
     * @memberof WorkspaceAnalysisTerminationResult
     */
    schema_id: WorkspaceAnalysisTerminationResultSchemaIdEnum;
    /**
     *
     * @type {WorkspaceAnalysisTerminationResultSchemaVersionEnum}
     * @memberof WorkspaceAnalysisTerminationResult
     */
    schema_version: WorkspaceAnalysisTerminationResultSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTerminationResult
     */
    model_run_ref: string | null;
    /**
     *
     * @type {WorkspaceAnalysisTerminationResultPayload}
     * @memberof WorkspaceAnalysisTerminationResult
     */
    payload: WorkspaceAnalysisTerminationResultPayload;
}


/**
 * @export
 */
export const WorkspaceAnalysisTerminationResultResultTypeEnum = {
    WorkspaceAnalysisTermination: 'workspace_analysis_termination'
} as const;
export type WorkspaceAnalysisTerminationResultResultTypeEnum = typeof WorkspaceAnalysisTerminationResultResultTypeEnum[keyof typeof WorkspaceAnalysisTerminationResultResultTypeEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTerminationResultSchemaIdEnum = {
    ConversationWorkspaceAnalysisTermination: 'conversation.workspace_analysis_termination'
} as const;
export type WorkspaceAnalysisTerminationResultSchemaIdEnum = typeof WorkspaceAnalysisTerminationResultSchemaIdEnum[keyof typeof WorkspaceAnalysisTerminationResultSchemaIdEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTerminationResultSchemaVersionEnum = {
    V1: 'v1'
} as const;
export type WorkspaceAnalysisTerminationResultSchemaVersionEnum = typeof WorkspaceAnalysisTerminationResultSchemaVersionEnum[keyof typeof WorkspaceAnalysisTerminationResultSchemaVersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTerminationResultPayload
 */
export interface WorkspaceAnalysisTerminationResultPayload {
    /**
     *
     * @type {WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum}
     * @memberof WorkspaceAnalysisTerminationResultPayload
     */
    termination_reason: WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTerminationResultPayload
     */
    summary: string;
}


/**
 * @export
 */
export const WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum = {
    WorkspaceAnalysisBudgetExhausted: 'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
    WorkspaceAnalysisReceiptInvalid: 'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
    WorkspaceAnalysisResultUnknown: 'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
    WorkspaceAnalysisDeadlineExceeded: 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
    WorkspaceAnalysisModelFailed: 'WORKSPACE_ANALYSIS_MODEL_FAILED',
    WorkspaceAnalysisToolFailed: 'WORKSPACE_ANALYSIS_TOOL_FAILED',
    WorkspaceAnalysisRuntimeFailed: 'WORKSPACE_ANALYSIS_RUNTIME_FAILED',
    WorkspaceAnalysisCancelled: 'WORKSPACE_ANALYSIS_CANCELLED'
} as const;
export type WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum = typeof WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum[keyof typeof WorkspaceAnalysisTerminationResultPayloadTerminationReasonEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimeline
 */
export interface WorkspaceAnalysisTimeline {
    /**
     *
     * @type {WorkspaceAnalysisTimelineSchemaIdEnum}
     * @memberof WorkspaceAnalysisTimeline
     */
    schema_id: WorkspaceAnalysisTimelineSchemaIdEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineSchemaVersionEnum}
     * @memberof WorkspaceAnalysisTimeline
     */
    schema_version: WorkspaceAnalysisTimelineSchemaVersionEnum;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTimeline
     */
    workspace_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTimeline
     */
    answer_id: string;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTimeline
     */
    analysis_run_id: string;
    /**
     *
     * @type {WorkspaceAnalysisTimelineRunStatusEnum}
     * @memberof WorkspaceAnalysisTimeline
     */
    run_status: WorkspaceAnalysisTimelineRunStatusEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineTerminationReasonEnum}
     * @memberof WorkspaceAnalysisTimeline
     */
    termination_reason: WorkspaceAnalysisTimelineTerminationReasonEnum | null;
    /**
     *
     * @type {Array<WorkspaceAnalysisTimelineItem>}
     * @memberof WorkspaceAnalysisTimeline
     */
    items: Array<WorkspaceAnalysisTimelineItem>;
    /**
     *
     * @type {WorkspaceAnalysisTimelineBudget}
     * @memberof WorkspaceAnalysisTimeline
     */
    budget: WorkspaceAnalysisTimelineBudget;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimeline
     */
    latest_server_event_sequence: number;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineSchemaIdEnum = {
    ConversationWorkspaceAnalysisTimeline: 'conversation.workspace_analysis_timeline'
} as const;
export type WorkspaceAnalysisTimelineSchemaIdEnum = typeof WorkspaceAnalysisTimelineSchemaIdEnum[keyof typeof WorkspaceAnalysisTimelineSchemaIdEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineSchemaVersionEnum = {
    V1: 'v1'
} as const;
export type WorkspaceAnalysisTimelineSchemaVersionEnum = typeof WorkspaceAnalysisTimelineSchemaVersionEnum[keyof typeof WorkspaceAnalysisTimelineSchemaVersionEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineRunStatusEnum = {
    Queued: 'queued',
    Running: 'running',
    Succeeded: 'succeeded',
    Refused: 'refused',
    ClarificationRequired: 'clarification_required',
    Failed: 'failed',
    Cancelled: 'cancelled'
} as const;
export type WorkspaceAnalysisTimelineRunStatusEnum = typeof WorkspaceAnalysisTimelineRunStatusEnum[keyof typeof WorkspaceAnalysisTimelineRunStatusEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineTerminationReasonEnum = {
    Completed: 'COMPLETED',
    WorkspaceAnalysisEvidenceInsufficient: 'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
    WorkspaceAnalysisCitationInvalid: 'WORKSPACE_ANALYSIS_CITATION_INVALID',
    WorkspaceAnalysisFaithfulnessRejected: 'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
    WorkspaceAnalysisClarificationRequired: 'WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED',
    WorkspaceAnalysisModelRefused: 'WORKSPACE_ANALYSIS_MODEL_REFUSED',
    WorkspaceAnalysisBudgetExhausted: 'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
    WorkspaceAnalysisReceiptInvalid: 'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
    WorkspaceAnalysisResultUnknown: 'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
    WorkspaceAnalysisDeadlineExceeded: 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
    WorkspaceAnalysisModelFailed: 'WORKSPACE_ANALYSIS_MODEL_FAILED',
    WorkspaceAnalysisToolFailed: 'WORKSPACE_ANALYSIS_TOOL_FAILED',
    WorkspaceAnalysisRuntimeFailed: 'WORKSPACE_ANALYSIS_RUNTIME_FAILED',
    WorkspaceAnalysisCancelled: 'WORKSPACE_ANALYSIS_CANCELLED'
} as const;
export type WorkspaceAnalysisTimelineTerminationReasonEnum = typeof WorkspaceAnalysisTimelineTerminationReasonEnum[keyof typeof WorkspaceAnalysisTimelineTerminationReasonEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineBudget
 */
export interface WorkspaceAnalysisTimelineBudget {
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    model_calls: WorkspaceAnalysisTimelineCounter;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    tool_calls: WorkspaceAnalysisTimelineCounter;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    source_reads: WorkspaceAnalysisTimelineCounter;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    input_tokens: WorkspaceAnalysisTimelineCounter;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    output_tokens: WorkspaceAnalysisTimelineCounter;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCounter}
     * @memberof WorkspaceAnalysisTimelineBudget
     */
    estimated_cost_microunits: WorkspaceAnalysisTimelineCounter | null;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineCitationSummary
 */
export interface WorkspaceAnalysisTimelineCitationSummary {
    /**
     *
     * @type {WorkspaceAnalysisTimelineCitationSummaryKindEnum}
     * @memberof WorkspaceAnalysisTimelineCitationSummary
     */
    kind: WorkspaceAnalysisTimelineCitationSummaryKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineCitationSummaryCitationValidation}
     * @memberof WorkspaceAnalysisTimelineCitationSummary
     */
    citation_validation: WorkspaceAnalysisTimelineCitationSummaryCitationValidation;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineCitationSummaryKindEnum = {
    CitationValidation: 'citation_validation'
} as const;
export type WorkspaceAnalysisTimelineCitationSummaryKindEnum = typeof WorkspaceAnalysisTimelineCitationSummaryKindEnum[keyof typeof WorkspaceAnalysisTimelineCitationSummaryKindEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineCitationSummaryCitationValidation
 */
export interface WorkspaceAnalysisTimelineCitationSummaryCitationValidation {
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineCitationSummaryCitationValidation
     */
    valid_count: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineCitationSummaryCitationValidation
     */
    invalid_count: number;
    /**
     *
     * @type {Array<string>}
     * @memberof WorkspaceAnalysisTimelineCitationSummaryCitationValidation
     */
    reason_codes: Array<string>;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineCounter
 */
export interface WorkspaceAnalysisTimelineCounter {
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineCounter
     */
    used: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineCounter
     */
    max: number;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineGitSummary
 */
export interface WorkspaceAnalysisTimelineGitSummary {
    /**
     *
     * @type {WorkspaceAnalysisTimelineGitSummaryKindEnum}
     * @memberof WorkspaceAnalysisTimelineGitSummary
     */
    kind: WorkspaceAnalysisTimelineGitSummaryKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisGitStatus}
     * @memberof WorkspaceAnalysisTimelineGitSummary
     */
    git: WorkspaceAnalysisGitStatus;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineGitSummaryKindEnum = {
    Git: 'git'
} as const;
export type WorkspaceAnalysisTimelineGitSummaryKindEnum = typeof WorkspaceAnalysisTimelineGitSummaryKindEnum[keyof typeof WorkspaceAnalysisTimelineGitSummaryKindEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineItem
 */
export interface WorkspaceAnalysisTimelineItem {
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    sequence: number;
    /**
     *
     * @type {WorkspaceAnalysisTimelineItemKindEnum}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    kind: WorkspaceAnalysisTimelineItemKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineItemPhaseEnum}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    phase: WorkspaceAnalysisTimelineItemPhaseEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineItemStatusEnum}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    status: WorkspaceAnalysisTimelineItemStatusEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRef}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    tool_ref: WorkspaceAnalysisTimelineToolRef | null;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    duration_ms: number | null;
    /**
     *
     * @type {WorkspaceAnalysisTimelineItemErrorCodeEnum}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    error_code: WorkspaceAnalysisTimelineItemErrorCodeEnum | null;
    /**
     *
     * @type {WorkspaceAnalysisTimelineItemSummary}
     * @memberof WorkspaceAnalysisTimelineItem
     */
    summary: WorkspaceAnalysisTimelineItemSummary | null;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineItemKindEnum = {
    Node: 'node',
    Model: 'model',
    Tool: 'tool'
} as const;
export type WorkspaceAnalysisTimelineItemKindEnum = typeof WorkspaceAnalysisTimelineItemKindEnum[keyof typeof WorkspaceAnalysisTimelineItemKindEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineItemPhaseEnum = {
    InspectWorkspace: 'inspect_workspace',
    RetrieveEvidence: 'retrieve_evidence',
    ReadEvidence: 'read_evidence',
    SynthesizeAnswer: 'synthesize_answer',
    ValidateCitations: 'validate_citations',
    ReviewPublish: 'review_publish'
} as const;
export type WorkspaceAnalysisTimelineItemPhaseEnum = typeof WorkspaceAnalysisTimelineItemPhaseEnum[keyof typeof WorkspaceAnalysisTimelineItemPhaseEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineItemStatusEnum = {
    Pending: 'pending',
    Waiting: 'waiting',
    Started: 'started',
    Succeeded: 'succeeded',
    Failed: 'failed',
    Refused: 'refused',
    Unknown: 'unknown',
    Cancelled: 'cancelled'
} as const;
export type WorkspaceAnalysisTimelineItemStatusEnum = typeof WorkspaceAnalysisTimelineItemStatusEnum[keyof typeof WorkspaceAnalysisTimelineItemStatusEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineItemErrorCodeEnum = {
    WorkspaceAnalysisEvidenceInsufficient: 'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
    WorkspaceAnalysisCitationInvalid: 'WORKSPACE_ANALYSIS_CITATION_INVALID',
    WorkspaceAnalysisFaithfulnessRejected: 'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
    WorkspaceAnalysisModelRefused: 'WORKSPACE_ANALYSIS_MODEL_REFUSED',
    WorkspaceAnalysisBudgetExhausted: 'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
    WorkspaceAnalysisReceiptInvalid: 'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
    WorkspaceAnalysisResultUnknown: 'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
    WorkspaceAnalysisDeadlineExceeded: 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
    WorkspaceAnalysisModelFailed: 'WORKSPACE_ANALYSIS_MODEL_FAILED',
    WorkspaceAnalysisToolFailed: 'WORKSPACE_ANALYSIS_TOOL_FAILED',
    WorkspaceAnalysisRuntimeFailed: 'WORKSPACE_ANALYSIS_RUNTIME_FAILED',
    WorkspaceAnalysisCancelled: 'WORKSPACE_ANALYSIS_CANCELLED'
} as const;
export type WorkspaceAnalysisTimelineItemErrorCodeEnum = typeof WorkspaceAnalysisTimelineItemErrorCodeEnum[keyof typeof WorkspaceAnalysisTimelineItemErrorCodeEnum];

/**
 * @type WorkspaceAnalysisTimelineItemSummary
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkspaceAnalysisTimelineItemSummary = WorkspaceAnalysisTimelineCitationSummary | WorkspaceAnalysisTimelineGitSummary | WorkspaceAnalysisTimelineModelSummary | WorkspaceAnalysisTimelineSearchSummary | WorkspaceAnalysisTimelineSourceSummary | null;

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineModelSummary
 */
export interface WorkspaceAnalysisTimelineModelSummary {
    /**
     *
     * @type {WorkspaceAnalysisTimelineModelSummaryKindEnum}
     * @memberof WorkspaceAnalysisTimelineModelSummary
     */
    kind: WorkspaceAnalysisTimelineModelSummaryKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineModelSummaryModelUsage}
     * @memberof WorkspaceAnalysisTimelineModelSummary
     */
    model_usage: WorkspaceAnalysisTimelineModelSummaryModelUsage;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineModelSummaryKindEnum = {
    ModelUsage: 'model_usage'
} as const;
export type WorkspaceAnalysisTimelineModelSummaryKindEnum = typeof WorkspaceAnalysisTimelineModelSummaryKindEnum[keyof typeof WorkspaceAnalysisTimelineModelSummaryKindEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineModelSummaryModelUsage
 */
export interface WorkspaceAnalysisTimelineModelSummaryModelUsage {
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineModelSummaryModelUsage
     */
    input_tokens: number;
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineModelSummaryModelUsage
     */
    output_tokens: number;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineSearchSummary
 */
export interface WorkspaceAnalysisTimelineSearchSummary {
    /**
     *
     * @type {WorkspaceAnalysisTimelineSearchSummaryKindEnum}
     * @memberof WorkspaceAnalysisTimelineSearchSummary
     */
    kind: WorkspaceAnalysisTimelineSearchSummaryKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineSearchSummarySearch}
     * @memberof WorkspaceAnalysisTimelineSearchSummary
     */
    search: WorkspaceAnalysisTimelineSearchSummarySearch;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineSearchSummaryKindEnum = {
    Search: 'search'
} as const;
export type WorkspaceAnalysisTimelineSearchSummaryKindEnum = typeof WorkspaceAnalysisTimelineSearchSummaryKindEnum[keyof typeof WorkspaceAnalysisTimelineSearchSummaryKindEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineSearchSummarySearch
 */
export interface WorkspaceAnalysisTimelineSearchSummarySearch {
    /**
     *
     * @type {number}
     * @memberof WorkspaceAnalysisTimelineSearchSummarySearch
     */
    hit_count: number;
    /**
     *
     * @type {Array<string>}
     * @memberof WorkspaceAnalysisTimelineSearchSummarySearch
     */
    degradation_codes: Array<string>;
}
/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineSourceSummary
 */
export interface WorkspaceAnalysisTimelineSourceSummary {
    /**
     *
     * @type {WorkspaceAnalysisTimelineSourceSummaryKindEnum}
     * @memberof WorkspaceAnalysisTimelineSourceSummary
     */
    kind: WorkspaceAnalysisTimelineSourceSummaryKindEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineSourceSummarySource}
     * @memberof WorkspaceAnalysisTimelineSourceSummary
     */
    source: WorkspaceAnalysisTimelineSourceSummarySource;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineSourceSummaryKindEnum = {
    Source: 'source'
} as const;
export type WorkspaceAnalysisTimelineSourceSummaryKindEnum = typeof WorkspaceAnalysisTimelineSourceSummaryKindEnum[keyof typeof WorkspaceAnalysisTimelineSourceSummaryKindEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineSourceSummarySource
 */
export interface WorkspaceAnalysisTimelineSourceSummarySource {
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTimelineSourceSummarySource
     */
    evidence_ref: string;
    /**
     *
     * @type {string}
     * @memberof WorkspaceAnalysisTimelineSourceSummarySource
     */
    content_hash: string;
    /**
     *
     * @type {boolean}
     * @memberof WorkspaceAnalysisTimelineSourceSummarySource
     */
    truncated: boolean;
}
/**
 * @type WorkspaceAnalysisTimelineToolRef
 *
 * @export
 */
// OpenAPI Generator 7.24.0 override: preserve the null branch of nullable oneOf aliases.
export type WorkspaceAnalysisTimelineToolRef = WorkspaceAnalysisTimelineToolRefOneOf | WorkspaceAnalysisTimelineToolRefOneOf1 | WorkspaceAnalysisTimelineToolRefOneOf2 | WorkspaceAnalysisTimelineToolRefOneOf3;

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineToolRefOneOf
 */
export interface WorkspaceAnalysisTimelineToolRefOneOf {
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOfNameEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf
     */
    name: WorkspaceAnalysisTimelineToolRefOneOfNameEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOfVersionEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf
     */
    version: WorkspaceAnalysisTimelineToolRefOneOfVersionEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOfNameEnum = {
    ReadGitStatus: 'ReadGitStatus'
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOfNameEnum = typeof WorkspaceAnalysisTimelineToolRefOneOfNameEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOfNameEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOfVersionEnum = {
    NUMBER_2: 2
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOfVersionEnum = typeof WorkspaceAnalysisTimelineToolRefOneOfVersionEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOfVersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineToolRefOneOf1
 */
export interface WorkspaceAnalysisTimelineToolRefOneOf1 {
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf1NameEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf1
     */
    name: WorkspaceAnalysisTimelineToolRefOneOf1NameEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf1
     */
    version: WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf1NameEnum = {
    SearchKnowledge: 'SearchKnowledge'
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf1NameEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf1NameEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf1NameEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum = {
    NUMBER_2: 2
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf1VersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineToolRefOneOf2
 */
export interface WorkspaceAnalysisTimelineToolRefOneOf2 {
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf2NameEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf2
     */
    name: WorkspaceAnalysisTimelineToolRefOneOf2NameEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf2
     */
    version: WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf2NameEnum = {
    ReadSource: 'ReadSource'
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf2NameEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf2NameEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf2NameEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum = {
    NUMBER_3: 3
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf2VersionEnum];

/**
 *
 * @export
 * @interface WorkspaceAnalysisTimelineToolRefOneOf3
 */
export interface WorkspaceAnalysisTimelineToolRefOneOf3 {
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf3NameEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf3
     */
    name: WorkspaceAnalysisTimelineToolRefOneOf3NameEnum;
    /**
     *
     * @type {WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum}
     * @memberof WorkspaceAnalysisTimelineToolRefOneOf3
     */
    version: WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum;
}


/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf3NameEnum = {
    ValidateCitation: 'ValidateCitation'
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf3NameEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf3NameEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf3NameEnum];

/**
 * @export
 */
export const WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum = {
    NUMBER_3: 3
} as const;
export type WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum = typeof WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum[keyof typeof WorkspaceAnalysisTimelineToolRefOneOf3VersionEnum];

/**
 *
 * @export
 * @interface WorkspaceScan
 */
export interface WorkspaceScan {
    /**
     *
     * @type {string}
     * @memberof WorkspaceScan
     */
    workspace_id: string;
    /**
     *
     * @type {Array<ScannedFile>}
     * @memberof WorkspaceScan
     */
    files: Array<ScannedFile>;
    /**
     *
     * @type {number}
     * @memberof WorkspaceScan
     */
    count: number;
}
