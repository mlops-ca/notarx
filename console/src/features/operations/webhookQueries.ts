import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback } from "react";
import { listWebhookDeliveries, listWebhookSubscriptions } from "../../client";
import type { WebhookDeliveryState } from "../../client";
import { unwrap } from "../../lib/query/client";
import { operationKeys } from "../../lib/query/keys";

/** One cursor page of subscriptions, by name. */
export function useWebhookSubscriptions(
  pageToken: string | undefined,
  pageSize: number,
) {
  return useQuery({
    queryKey: [
      ...operationKeys.webhookSubscriptions(),
      { pageToken, pageSize },
    ] as const,
    queryFn: ({ signal }) =>
      unwrap(
        listWebhookSubscriptions({
          query: { limit: pageSize, pageToken },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
  });
}

/**
 * One cursor page of deliveries for one state filter. Keyed by the filter and
 * the cursor, so a slower answer for a previous filter can never replace the
 * current one; the previous rows stay on screen while the next page loads.
 * Deliveries progress on the server, so they are always re-read.
 */
export function useWebhookDeliveries(
  state: WebhookDeliveryState | "all",
  pageToken: string | undefined,
  pageSize: number,
) {
  return useQuery({
    queryKey: [
      ...operationKeys.webhookDeliveries(state),
      { pageToken, pageSize },
    ] as const,
    queryFn: ({ signal }) =>
      unwrap(
        listWebhookDeliveries({
          query: {
            state: state === "all" ? undefined : state,
            limit: pageSize,
            pageToken,
          },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
    staleTime: 0,
  });
}

/** Re-read subscriptions and every delivery filter after a write. */
export function useInvalidateWebhooks() {
  const queryClient = useQueryClient();
  return useCallback(
    () =>
      Promise.all([
        queryClient.invalidateQueries({
          queryKey: operationKeys.webhookSubscriptions(),
        }),
        queryClient.invalidateQueries({
          queryKey: [...operationKeys.all, "webhook-deliveries"],
        }),
      ]),
    [queryClient],
  );
}
