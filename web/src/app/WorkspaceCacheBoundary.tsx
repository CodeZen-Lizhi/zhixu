import { useQueryClient } from "@tanstack/react-query";
import { type PropsWithChildren, useEffect, useRef } from "react";

import { useActiveWorkspaceId } from "./active-workspace";
import { clearWorkspaceRuntimeState } from "./workspace-runtime-state";

export const WorkspaceCacheBoundary = ({ children }: PropsWithChildren) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const previousWorkspaceId = useRef(workspaceId);

  useEffect(() => {
    const previous = previousWorkspaceId.current;
    if (previous !== workspaceId) {
      void clearWorkspaceRuntimeState(queryClient, previous);
    }
    previousWorkspaceId.current = workspaceId;
  }, [queryClient, workspaceId]);

  return children;
};
