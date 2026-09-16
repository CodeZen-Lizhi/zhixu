import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import { promoteSynthesisSource, type PromoteSynthesisSourceInput } from "../../api/synthesis-goals";
import { getActiveWorkspaceId } from "../../app/active-workspace";
import { Button, ErrorState } from "../../shared/ui";

export const SourcePromotion = ({ workspaceId, sourceVersionId }: { workspaceId: string; sourceVersionId: string }) => {
  const queryClient = useQueryClient();
  const attempt = useRef<{ key: string; controller: AbortController } | null>(null);
  const promote = useMutation({
    mutationFn: (input: PromoteSynthesisSourceInput) => promoteSynthesisSource(input), retry: false,
    onSuccess: async (_result, input) => {
      if (getActiveWorkspaceId() !== input.workspaceId) return;
      await queryClient.invalidateQueries({ queryKey: ["synthesis", input.workspaceId, "goals"] });
    },
  });
  useEffect(() => () => { attempt.current?.controller.abort(); attempt.current = null; }, [workspaceId, sourceVersionId]);
  const start = () => {
    if (promote.isPending || promote.isSuccess || workspaceId !== getActiveWorkspaceId()) return;
    attempt.current ??= { key: `source-promotion-${crypto.randomUUID()}`, controller: new AbortController() };
    promote.mutate({ workspaceId, sourceVersionId, idempotencyKey: attempt.current.key, signal: attempt.current.controller.signal });
  };
  return <section aria-label="从这份笔记建立主笔记">
    <h3>持续整理这份笔记</h3>
    <p>以这份笔记为基础生成可审阅的主笔记；之后可由 AI 提炼维护范围，持续汇入相关知识。</p>
    {promote.isSuccess ? <p role="status">整理请求已保存。<Link to="/authoring/notes">到主笔记中心查看进度</Link></p> : <Button variant="secondary" onClick={start} disabled={promote.isPending}>{promote.isPending ? "正在保存整理请求…" : "从这份笔记建立主笔记"}</Button>}
    {promote.isError ? <ErrorState title="整理请求尚未确认" description={promote.error.message} onRetry={start} /> : null}
  </section>;
};
