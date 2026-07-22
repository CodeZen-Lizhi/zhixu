import { useQueryClient } from "@tanstack/react-query";
import { type PropsWithChildren, useEffect, useRef } from "react";

import { clearCollectionWorkspaceQueries } from "../features/collections/queries";
import { clearGraphWorkspaceQueries } from "../features/graph/queries";
import { clearSemanticLinkWorkspaceQueries } from "../features/graph/semantic-link-queries";
import { clearHealthWorkspaceQueries } from "../features/health/queries";
import { clearSearchWorkspaceQueries } from "../features/search/query-keys";
import { useActiveWorkspaceId } from "./active-workspace";

export const WorkspaceCacheBoundary = ({ children }: PropsWithChildren) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const previousWorkspaceId = useRef(workspaceId);

  useEffect(() => {
    const previous = previousWorkspaceId.current;
    if (previous !== workspaceId) {
      clearGraphWorkspaceQueries(queryClient, previous);
      clearSemanticLinkWorkspaceQueries(queryClient, previous);
      clearCollectionWorkspaceQueries(queryClient, previous);
      clearHealthWorkspaceQueries(queryClient, previous);
      clearSearchWorkspaceQueries(queryClient, previous);
    }
    previousWorkspaceId.current = workspaceId;
  }, [queryClient, workspaceId]);

  return children;
};
