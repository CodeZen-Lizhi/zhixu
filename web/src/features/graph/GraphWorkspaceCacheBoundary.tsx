import { useQueryClient } from "@tanstack/react-query";
import { type PropsWithChildren, useEffect, useRef } from "react";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import { clearGraphWorkspaceQueries } from "./queries";
import { clearSemanticLinkWorkspaceQueries } from "./semantic-link-queries";

/** 在路由切换期间保持挂载，并在 Workspace 变化时清理旧 Graph Server State。 */
export const GraphWorkspaceCacheBoundary = ({ children }: PropsWithChildren) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const previousWorkspaceId = useRef(workspaceId);

  useEffect(() => {
    const previous = previousWorkspaceId.current;
    if (previous !== workspaceId) {
      clearGraphWorkspaceQueries(queryClient, previous);
      clearSemanticLinkWorkspaceQueries(queryClient, previous);
    }
    previousWorkspaceId.current = workspaceId;
  }, [queryClient, workspaceId]);

  return children;
};
