import { useEffect, useRef, useState } from "react";

import {
  connectAnswerDraftStream,
  type AnswerDraftConnectionState,
  type AnswerDraftStreamEvent,
} from "../../events/answer-draft-stream";

const maximumDraftBytes = 4 * 1024 * 1024;

export interface AnswerDraftState {
  answerId: string;
  generation: number | null;
  sequence: number;
  content: string;
  bytes: number;
  connectionState: AnswerDraftConnectionState;
}

export type AnswerDraftAction =
  | { type: "bind"; answerId: string }
  | { type: "connection"; answerId: string; state: AnswerDraftConnectionState }
  | { type: "event"; answerId: string; event: AnswerDraftStreamEvent }
  | { type: "clear"; answerId: string };

export class AnswerDraftStateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AnswerDraftStateError";
  }
}

export const emptyAnswerDraft = (answerId = ""): AnswerDraftState => ({
  answerId,
  generation: null,
  sequence: 0,
  content: "",
  bytes: 0,
  connectionState: "closed",
});

export const answerDraftReducer = (state: AnswerDraftState, action: AnswerDraftAction): AnswerDraftState => {
  if (action.type === "bind") return action.answerId === state.answerId ? state : emptyAnswerDraft(action.answerId);
  if (action.answerId !== state.answerId) return state;
  if (action.type === "clear") return emptyAnswerDraft(action.answerId);
  if (action.type === "connection") return { ...state, connectionState: action.state };
  const event = action.event;
  if (event.type !== "chunk") return emptyAnswerDraft(action.answerId);
  const contiguous = state.generation === null
    ? event.sequence === 1
    : event.generation === state.generation && event.sequence === state.sequence + 1;
  if (!contiguous) throw new AnswerDraftStateError("草稿流 generation/sequence 不连续");
  const chunkBytes = new TextEncoder().encode(event.content).length;
  if (state.bytes + chunkBytes > maximumDraftBytes) throw new AnswerDraftStateError("草稿正文超过浏览器上限");
  return {
    answerId: state.answerId,
    generation: event.generation,
    sequence: event.sequence,
    content: state.content + event.content,
    bytes: state.bytes + chunkBytes,
    connectionState: "open",
  };
};

export interface UsePendingAnswerDraftOptions {
  workspaceId: string;
  answerId: string;
  enabled: boolean;
  refetchAnswer: () => Promise<void>;
}

export const usePendingAnswerDraft = ({
  workspaceId,
  answerId,
  enabled,
  refetchAnswer,
}: UsePendingAnswerDraftOptions): AnswerDraftState => {
  const [state, setState] = useState<AnswerDraftState>(() => emptyAnswerDraft(answerId));
  const stateRef = useRef(state);
  const refetchRef = useRef(refetchAnswer);
  refetchRef.current = refetchAnswer;

  useEffect(() => {
    let active = true;
    const initial = emptyAnswerDraft(answerId);
    stateRef.current = initial;
    setState(initial);
    if (!enabled || workspaceId === "" || answerId === "") return () => { active = false; };

    const apply = (action: AnswerDraftAction): void => {
      if (!active) return;
      const next = answerDraftReducer(stateRef.current, action);
      stateRef.current = next;
      setState(next);
    };
    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      onStateChange: (connectionState) => apply({ type: "connection", answerId, state: connectionState }),
      onEvent: (event) => apply({ type: "event", answerId, event }),
      onRecoveryRequired: async () => {
        apply({ type: "clear", answerId });
        await refetchRef.current();
      },
    });
    return () => {
      active = false;
      connection.close();
    };
  }, [answerId, enabled, workspaceId]);

  return state.answerId === answerId ? state : emptyAnswerDraft(answerId);
};
