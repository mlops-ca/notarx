import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { listUserGroupMembers, listUserGroups } from "../../../client";
import { unwrap } from "../../../lib/query/client";
import { identityKeys } from "../../../lib/query/keys";

const PAGE_SIZE = 100;

/** User groups, one keyset page at a time. */
export function useUserGroupPages() {
  return useInfiniteQuery({
    queryKey: identityKeys.userGroups(),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        listUserGroups({
          query: { limit: PAGE_SIZE, pageToken: pageParam },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextPageToken || undefined,
  });
}

/** The members of one group, one keyset page at a time. */
export function useUserGroupMemberPages(groupId: string, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: identityKeys.userGroupMembers(groupId),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        listUserGroupMembers({
          path: { groupId },
          query: { limit: PAGE_SIZE, pageToken: pageParam },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextPageToken || undefined,
    enabled,
  });
}

/**
 * Refreshes every list a group change can alter: the groups, their members,
 * and the principal choices of the grant editors.
 */
export function useInvalidateUserGroups() {
  const queryClient = useQueryClient();
  return useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: identityKeys.userGroups() }),
      queryClient.invalidateQueries({ queryKey: ["access", "directory"] }),
    ]);
  }, [queryClient]);
}
