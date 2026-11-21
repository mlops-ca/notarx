import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { listRemoteInstances } from "../../client";
import { unwrap } from "../../lib/query/client";
import { remoteInstanceKeys } from "../../lib/query/keys";

const PAGE_SIZE = 100;

/** Registered remote instances, one cursor page at a time. */
export function useRemoteInstancePages() {
  return useInfiniteQuery({
    queryKey: remoteInstanceKeys.list(),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        listRemoteInstances({
          query: { limit: PAGE_SIZE, pageToken: pageParam },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextPageToken || undefined,
  });
}

export function useInvalidateRemoteInstances() {
  const queryClient = useQueryClient();
  return useCallback(
    () => queryClient.invalidateQueries({ queryKey: remoteInstanceKeys.all }),
    [queryClient],
  );
}
