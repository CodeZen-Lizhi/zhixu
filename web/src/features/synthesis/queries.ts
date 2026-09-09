import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getNoteInterviewPreparation, getSynthesisNote, getSynthesisProcessing, getSynthesisRevision,
  listNoteInterviewPreparations, listSynthesisNotes, listSynthesisProcessing, listSynthesisRevisions, openSynthesisSource, retrySynthesisProcessing,
  type SynthesisSourceRef,
} from "../../api/synthesis";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";

export const synthesisQueryKeys = {
  all: (workspaceId: string) => ["synthesis", workspaceId] as const,
  notes: (workspaceId: string) => ["synthesis", workspaceId, "notes"] as const,
  note: (workspaceId: string, noteId: string) => ["synthesis", workspaceId, "note", noteId] as const,
  revisions: (workspaceId: string, noteId: string) => ["synthesis", workspaceId, "revisions", noteId] as const,
  revision: (workspaceId: string, noteId: string, revisionId: string) => ["synthesis", workspaceId, "revision", noteId, revisionId] as const,
  processing: (workspaceId: string) => ["synthesis", workspaceId, "processing"] as const,
  process: (workspaceId: string, processingId: string) => ["synthesis", workspaceId, "process", processingId] as const,
  preparation: (workspaceId: string, noteId: string, preparationId: string) => ["synthesis", workspaceId, "interview", noteId, preparationId] as const,
  preparations: (workspaceId: string, noteId: string) => ["synthesis", workspaceId, "interviews", noteId] as const,
};
const pageStart = (): string | null => null;
const noteActive = (status: string) => status === "QUEUED" || status === "GENERATING";
const processingActive = (status: string) => status === "PENDING" || status === "RUNNING";

export const useSynthesisNotes = () => {
  const workspaceId = useActiveWorkspaceId();
  return useInfiniteQuery({
    queryKey: synthesisQueryKeys.notes(workspaceId),
    queryFn: ({ signal, pageParam }) => listSynthesisNotes(workspaceId, pageParam, signal),
    initialPageParam: pageStart(), getNextPageParam: (page) => page.nextCursor,
    enabled: workspaceId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.pages.some((page) => page.items.some((item) => noteActive(item.note.status))) ? 3000 : false,
  });
};
export const useSynthesisProcessingList = () => {
  const workspaceId = useActiveWorkspaceId();
  return useInfiniteQuery({
    queryKey: synthesisQueryKeys.processing(workspaceId),
    queryFn: ({ signal, pageParam }) => listSynthesisProcessing(workspaceId, pageParam, signal),
    initialPageParam: pageStart(), getNextPageParam: (page) => page.nextCursor,
    enabled: workspaceId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.pages.some((page) => page.items.some((item) => processingActive(item.status))) ? 3000 : false,
  });
};
export const useSynthesisNote = (noteId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: synthesisQueryKeys.note(workspaceId, noteId), queryFn: ({ signal }) => getSynthesisNote(workspaceId, noteId, signal),
    enabled: workspaceId !== "" && noteId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data && (noteActive(query.state.data.note.status) || query.state.data.note.status === "PENDING_APPROVAL") ? 3000 : false });
};
export const useSynthesisRevisions = (noteId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useInfiniteQuery({ queryKey: synthesisQueryKeys.revisions(workspaceId, noteId),
    queryFn: ({ signal, pageParam }) => listSynthesisRevisions(workspaceId, noteId, pageParam, signal),
    initialPageParam: pageStart(), getNextPageParam: (page) => page.nextCursor,
    enabled: workspaceId !== "" && noteId !== "", retry: false });
};
export const useSynthesisRevision = (noteId: string, revisionId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: synthesisQueryKeys.revision(workspaceId, noteId, revisionId), queryFn: ({ signal }) => getSynthesisRevision(workspaceId, noteId, revisionId, signal),
    enabled: workspaceId !== "" && noteId !== "" && revisionId !== "", retry: false });
};
export const useSynthesisSource = (noteId: string, revisionId: string, reference: SynthesisSourceRef | null) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: ["synthesis", workspaceId, "source", noteId, revisionId, reference?.sourceSpanId ?? "", reference?.excerptHash ?? ""],
    queryFn: ({ signal }) => {
      if (reference === null) throw new Error("尚未选择来源。");
      return openSynthesisSource(workspaceId, noteId, revisionId, reference, signal);
    }, enabled: workspaceId !== "" && reference !== null && reference.source.workspaceId === workspaceId, retry: false, gcTime: 0 });
};
export const useSynthesisProcessing = (processingId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: synthesisQueryKeys.process(workspaceId, processingId), queryFn: ({ signal }) => getSynthesisProcessing(workspaceId, processingId, signal),
    enabled: workspaceId !== "" && processingId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data && processingActive(query.state.data.status) ? 3000 : false });
};
export const useRetrySynthesis = () => {
  const queryClient = useQueryClient();
  return useMutation({ mutationFn: retrySynthesisProcessing, retry: false,
    onSuccess: async (result, input) => {
      if (getActiveWorkspaceId() !== input.workspaceId) return;
      queryClient.setQueryData(synthesisQueryKeys.process(input.workspaceId, result.id), result);
      await queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.all(input.workspaceId) });
    } });
};
export const useNoteInterviewPreparation = (noteId: string, preparationId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: synthesisQueryKeys.preparation(workspaceId, noteId, preparationId),
    queryFn: ({ signal }) => getNoteInterviewPreparation(workspaceId, noteId, preparationId, signal),
    enabled: workspaceId !== "" && noteId !== "" && preparationId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data && noteActive(query.state.data.status) ? 3000 : false });
};
export const useNoteInterviewPreparations = (noteId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: synthesisQueryKeys.preparations(workspaceId, noteId),
    queryFn: ({ signal }) => listNoteInterviewPreparations(workspaceId, noteId, signal),
    enabled: workspaceId !== "" && noteId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.some((preparation) => noteActive(preparation.status)) ? 3000 : false });
};
