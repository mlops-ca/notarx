import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { listWorkloadIssuers, listWorkloadTrustPolicies } from "../../client";
import { unwrap } from "../../lib/query/client";
import { workloadKeys } from "../../lib/query/keys";

const PAGE_SIZE = 200;

/** Trusted issuers, one cursor page at a time. */
export function useWorkloadIssuerPages() {
  return useInfiniteQuery({
    queryKey: workloadKeys.issuers(),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        listWorkloadIssuers({
          query: { pageSize: PAGE_SIZE, pageToken: pageParam },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextPageToken || undefined,
  });
}

/** Trust policies, one cursor page at a time. */
export function useWorkloadPolicyPages() {
  return useInfiniteQuery({
    queryKey: workloadKeys.policies(),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        listWorkloadTrustPolicies({
          query: { pageSize: PAGE_SIZE, pageToken: pageParam },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextPageToken || undefined,
  });
}

/** Refreshes both lists: an issuer change can change what a policy shows. */
export function useInvalidateWorkload() {
  const queryClient = useQueryClient();
  return useCallback(
    () => queryClient.invalidateQueries({ queryKey: workloadKeys.all }),
    [queryClient],
  );
}
