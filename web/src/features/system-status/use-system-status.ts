import { useQuery } from "@tanstack/react-query";

import { fetchSystemStatus } from "../../api/system-status";

export const systemStatusQueryKey = ["system", "status"] as const;

export const useSystemStatus = () =>
  useQuery({
    queryKey: systemStatusQueryKey,
    queryFn: ({ signal }) => fetchSystemStatus(signal),
    retry: false,
    refetchInterval: 30_000,
  });
