import { Check, FilePenLine, Plus, RefreshCw, RotateCcw, Save, X, XCircle } from "lucide-react";
import { useRef, useState } from "react";

import {
  ReviewApiError,
  type CreateReviewCardInput,
  type EditReviewCardInput,
  type ReviewCard,
  type ReviewCardDecisionInput,
  type ReviewCardType,
  type ReviewDeck,
} from "../../api/review";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState } from "../../shared/ui";
import {
  useApproveReviewCard,
  useCreateReviewCard,
  useEditReviewCard,
  useRejectReviewCard,
  useReviewCards,
} from "./queries";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const cardTypes: readonly ReviewCardType[] = ["SHORT_ANSWER", "CLOZE", "COMPARISON", "SCENARIO", "CODE_READING", "DESIGN"];
const textBytes = (value: string): number => new TextEncoder().encode(value).length;
const errorText = (value: unknown): string => value instanceof Error ? value.message : "请求未完成，请重试。";
const isRetryable = (value: unknown): value is ReviewApiError => value instanceof ReviewApiError && value.retryable;
const statusTone = (status: ReviewCard["status"]): "success" | "warning" | "neutral" | "danger" => {
  if (status === "APPROVED") return "success";
  if (status === "DRAFT") return "warning";
  if (status === "INVALIDATED") return "danger";
  return "neutral";
};

interface CardDraft {
  claimId: string;
  question: string;
  answerPoints: string;
  sourceVersionId: string;
  sourceSpanId: string;
  evidenceHash: string;
  cardType: ReviewCardType;
  difficulty: string;
  modelVersion: string;
}

const emptyDraft = (): CardDraft => ({
  claimId: "",
  question: "",
  answerPoints: "",
  sourceVersionId: "",
  sourceSpanId: "",
  evidenceHash: "",
  cardType: "SHORT_ANSWER",
  difficulty: "0.5",
  modelVersion: "manual",
});

const draftFromCard = (card: ReviewCard): CardDraft => ({
  claimId: card.claimId,
  question: card.question,
  answerPoints: card.answerPoints.join("\n"),
  sourceVersionId: card.evidence[0]?.sourceVersionId ?? "",
  sourceSpanId: card.evidence[0]?.sourceSpanId ?? "",
  evidenceHash: card.evidence[0]?.evidenceHash ?? "",
  cardType: card.cardType,
  difficulty: String(card.difficulty),
  modelVersion: card.modelVersion,
});

const commandKey = (prefix: string, signature: string, attempts: Map<string, string>): string => {
  const existing = attempts.get(signature);
  if (existing !== undefined) return existing;
  const value = `${prefix}-${crypto.randomUUID()}`;
  attempts.set(signature, value);
  return value;
};

export const ReviewCardsPanel = ({ decks }: { decks: ReviewDeck[] }) => {
  const workspaceId = useActiveWorkspaceId();
  const [deckSelection, setDeckSelection] = useState("");
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingCard, setEditingCard] = useState<ReviewCard>();
  const [draft, setDraft] = useState<CardDraft>(() => emptyDraft());
  const [formError, setFormError] = useState<string>();
  const attempts = useRef(new Map<string, string>());
  const selectedDeckId = decks.some((deck) => deck.id === deckSelection) ? deckSelection : decks[0]?.id ?? "";
  const cards = useReviewCards(selectedDeckId);
  const create = useCreateReviewCard();
  const edit = useEditReviewCard();
  const approve = useApproveReviewCard();
  const reject = useRejectReviewCard();
  const commandError = create.error ?? edit.error ?? approve.error ?? reject.error;
  const pending = create.isPending || edit.isPending || approve.isPending || reject.isPending;

  const closeEditor = (): void => {
    setEditorOpen(false);
    setEditingCard(undefined);
    setDraft(emptyDraft());
    setFormError(undefined);
  };
  const openCreate = (): void => {
    setEditingCard(undefined);
    setDraft(emptyDraft());
    setFormError(undefined);
    setEditorOpen(true);
  };
  const openEdit = (card: ReviewCard): void => {
    setEditingCard(card);
    setDraft(draftFromCard(card));
    setFormError(undefined);
    setEditorOpen(true);
  };
  const updateDraft = <K extends keyof CardDraft>(field: K, value: CardDraft[K]): void => {
    setDraft((current) => ({ ...current, [field]: value }));
  };

  const buildInput = (): CreateReviewCardInput | EditReviewCardInput => {
    if (workspaceId === "" || !uuidPattern.test(selectedDeckId) || !uuidPattern.test(draft.claimId) ||
      !uuidPattern.test(draft.sourceVersionId) || !uuidPattern.test(draft.sourceSpanId) || !hashPattern.test(draft.evidenceHash)) {
      throw new Error("Deck、Claim 与 Evidence 标识必须完整且有效。");
    }
    const question = draft.question.trim();
    const answerPoints = draft.answerPoints.split("\n").map((value) => value.trim()).filter(Boolean);
    const difficulty = Number(draft.difficulty);
    const modelVersion = draft.modelVersion.trim();
    if (question === "" || textBytes(question) > 8192) throw new Error("问题必须为 1 到 8192 字节。");
    if (answerPoints.length === 0 || answerPoints.length > 128 || new Set(answerPoints).size !== answerPoints.length || answerPoints.some((value) => textBytes(value) > 4096)) {
      throw new Error("答案要点必须逐行填写、保持唯一，且每项不超过 4096 字节。");
    }
    if (!Number.isFinite(difficulty) || difficulty < 0 || difficulty > 1) throw new Error("难度必须在 0 到 1 之间。");
    if (modelVersion === "" || textBytes(modelVersion) > 8192) throw new Error("模型版本无效。");
    const primaryEvidence = {
      schemaVersion: "review-evidence/v1" as const,
      claimId: draft.claimId,
      sourceVersionId: draft.sourceVersionId,
      sourceSpanId: draft.sourceSpanId,
      evidenceHash: draft.evidenceHash,
    };
    const base = {
      workspaceId,
      deckId: selectedDeckId,
      claimId: draft.claimId,
      question,
      answerPoints,
      evidence: editingCard === undefined ? [primaryEvidence] : [primaryEvidence, ...editingCard.evidence.slice(1)],
      cardType: draft.cardType,
      difficulty,
      modelVersion,
    };
    const signature = JSON.stringify({ ...base, cardId: editingCard?.id, expectedVersion: editingCard?.version });
    const idempotencyKey = commandKey(editingCard === undefined ? "review-card-create" : "review-card-edit", signature, attempts.current);
    return editingCard === undefined
      ? { ...base, idempotencyKey }
      : { ...base, cardId: editingCard.id, expectedVersion: editingCard.version, idempotencyKey };
  };

  const submit = (): void => {
    setFormError(undefined);
    try {
      const input = buildInput();
      const signature = JSON.stringify({ ...input, idempotencyKey: undefined });
      const onSuccess = (): void => {
        attempts.current.delete(signature);
        closeEditor();
      };
      if ("cardId" in input) edit.mutate(input, { onSuccess });
      else create.mutate(input, { onSuccess });
    } catch (error: unknown) {
      setFormError(errorText(error));
    }
  };

  const decide = (card: ReviewCard, action: "approve" | "reject"): void => {
    const signature = `${workspaceId}:${card.id}:${String(card.version)}:${action}`;
    const input: ReviewCardDecisionInput = {
      workspaceId,
      cardId: card.id,
      expectedVersion: card.version,
      reason: "",
      idempotencyKey: commandKey(`review-card-${action}`, signature, attempts.current),
    };
    const onSuccess = (): void => { attempts.current.delete(signature); };
    if (action === "approve") approve.mutate(input, { onSuccess });
    else reject.mutate(input, { onSuccess });
  };

  const retryCommand = (): void => {
    if (create.variables !== undefined) create.mutate(create.variables);
    else if (edit.variables !== undefined) edit.mutate(edit.variables);
    else if (approve.variables !== undefined) approve.mutate(approve.variables);
    else if (reject.variables !== undefined) reject.mutate(reject.variables);
  };

  return <Card>
    <CardHeader
      eyebrow="Review Cards"
      title="Card 管理"
      description="Card 绑定正式 Claim 与 Evidence；编辑后回到 DRAFT，重新审批才进入调度。"
      action={<div className="button-row"><Button variant="ghost" onClick={() => void cards.refetch()} disabled={cards.isFetching || selectedDeckId === ""} aria-label="刷新 Card 列表"><RefreshCw size={16} /></Button><Button size="sm" onClick={openCreate} disabled={selectedDeckId === ""}><Plus size={15} />新建 Card</Button></div>}
    />
    {decks.length === 0 ? <EmptyState title="没有可管理的 Deck" description="创建 Deck 后可在这里维护 Card。" /> : <>
      <label className="review-card-deck-select">当前 Deck<select value={selectedDeckId} onChange={(event) => { setDeckSelection(event.target.value); closeEditor(); }} disabled={pending}>{decks.map((deck) => <option key={deck.id} value={deck.id}>{deck.name} · {deck.status}</option>)}</select></label>

      {editorOpen ? <form className="artifact-form review-card-editor" onSubmit={(event) => { event.preventDefault(); submit(); }}>
        <div className="artifact-form__wide review-card-editor__heading"><div><strong>{editingCard === undefined ? "新建 DRAFT Card" : "编辑 Card"}</strong>{editingCard === undefined ? null : <span>当前版本 {String(editingCard.version)}；保存后需要重新审批。</span>}</div><Button type="button" variant="ghost" size="sm" onClick={closeEditor} aria-label="关闭 Card 编辑器"><X size={15} /></Button></div>
        <label>Claim ID<input value={draft.claimId} onChange={(event) => updateDraft("claimId", event.target.value.trim())} spellCheck={false} /></label>
        <label>Card 类型<select value={draft.cardType} onChange={(event) => updateDraft("cardType", event.target.value as ReviewCardType)}>{cardTypes.map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
        <label className="artifact-form__wide">问题<textarea value={draft.question} onChange={(event) => updateDraft("question", event.target.value)} /></label>
        <label className="artifact-form__wide">答案要点（每行一项）<textarea value={draft.answerPoints} onChange={(event) => updateDraft("answerPoints", event.target.value)} /></label>
        <label>Source Version ID<input value={draft.sourceVersionId} onChange={(event) => updateDraft("sourceVersionId", event.target.value.trim())} spellCheck={false} /></label>
        <label>Source Span ID<input value={draft.sourceSpanId} onChange={(event) => updateDraft("sourceSpanId", event.target.value.trim())} spellCheck={false} /></label>
        <label className="artifact-form__wide">Evidence Hash<input value={draft.evidenceHash} onChange={(event) => updateDraft("evidenceHash", event.target.value.trim())} spellCheck={false} /></label>
        <label>难度<input type="number" min="0" max="1" step="0.05" value={draft.difficulty} onChange={(event) => updateDraft("difficulty", event.target.value)} /></label>
        <label>模型版本<input value={draft.modelVersion} onChange={(event) => updateDraft("modelVersion", event.target.value)} /></label>
        <div className="artifact-form__wide button-row"><Button type="submit" disabled={pending}><Save size={15} />{editingCard === undefined ? "创建 DRAFT" : "保存并重新校验证据"}</Button><Button type="button" variant="secondary" onClick={closeEditor} disabled={pending}>取消</Button></div>
        {formError === undefined ? null : <p className="artifact-form__wide form-error" role="alert">{formError}</p>}
      </form> : null}

      {cards.isPending ? <div className="ui-state" role="status"><strong>正在读取 Card</strong><p>从服务端恢复当前 Deck 的 Card 状态。</p></div> : null}
      {cards.isError ? <ErrorState title="Card 列表不可用" description={errorText(cards.error)} onRetry={() => void cards.refetch()} /> : null}
      {!cards.isPending && !cards.isError && cards.data.items.length === 0 ? <EmptyState title="这个 Deck 还没有 Card" description="创建并审批第一张 Card 后，它会进入服务端调度。" action={<Button size="sm" onClick={openCreate}><Plus size={15} />新建 Card</Button>} /> : null}
      {!cards.isPending && !cards.isError && cards.data.items.length > 0 ? <div className="review-card-list">{cards.data.items.map((card) => <article key={card.id} className="review-card-row">
        <div className="review-card-row__body"><div className="review-card-row__title"><strong>{card.question}</strong><Badge tone={statusTone(card.status)}>{card.status}</Badge></div><small>{card.cardType} · 难度 {String(Math.round(card.difficulty * 100))}% · v{String(card.version)}</small><code>Claim {card.claimId} · Evidence {card.evidence[0]?.evidenceHash.slice(0, 16)}…</code>{card.invalidationReason === undefined ? null : <p className="form-error">{card.invalidationReason}</p>}</div>
        <div className="button-row review-card-row__actions"><Button size="sm" variant="ghost" onClick={() => openEdit(card)} disabled={pending}><FilePenLine size={14} />编辑</Button>{card.status === "DRAFT" ? <><Button size="sm" onClick={() => decide(card, "approve")} disabled={pending}><Check size={14} />审批</Button><Button size="sm" variant="secondary" onClick={() => decide(card, "reject")} disabled={pending}><XCircle size={14} />驳回</Button></> : null}</div>
      </article>)}</div> : null}
      {commandError === null ? null : <div className="ui-state ui-state--error" role="alert"><strong>Card 操作未完成</strong><p>{errorText(commandError)}</p><div className="button-row"><Button variant="secondary" onClick={() => void cards.refetch()}><RefreshCw size={15} />刷新 Card</Button>{isRetryable(commandError) ? <Button variant="secondary" onClick={retryCommand}><RotateCcw size={15} />重试原请求</Button> : null}</div></div>}
    </>}
  </Card>;
};
