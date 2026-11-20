import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback } from "react";
import {
  cancelRepositoryLifecycleJob,
  listAuditRetentionJobs,
  listLifecycleJobs,
  retryRepositoryLifecycleJob,
  runRepositoryLifecycleJobNow,
  type ListAuditRetentionJobsResponse,
  type RepositoryLifecycleJobPage,
} from "../../client";
import { unwrap } from "../../lib/query/client";
import { operationKeys } from "../../lib/query/keys";
import { usePreferences } from "../../lib/preferences";
import {
  ACTIVE_JOB_STATES,
  toOperationRows,
  type JobAction,
  type OperationRow,
} from "./operationModel";

const POLL_INTERVAL = 15_000;

/**
 * One cursor page of lifecycle jobs, newest first. The audit-retention jobs
 * come from an unpaged endpoint, so they stay out of the paged rows and are
 * returned separately (`auditRows`) with the first page only: later pages
 * neither repeat nor count them. While `poll`
 * is on and any job read is still active, the data is re-read every 15
 * seconds; jobs advance on the server without anything in the Console
 * invalidating them.
 */
export function useOperationJobs({
  poll,
  pageSize,
  pageToken,
}: {
  poll: boolean;
  pageSize: number;
  pageToken: string | undefined;
}) {
  const { t } = usePreferences();
  // Selector failures are query errors, so the panel stays mounted and
  // its Retry can fetch a corrected answer instead of reusing bad data.
  const select = useCallback(
    (data: {
      lifecycle: RepositoryLifecycleJobPage;
      auditRetention: ListAuditRetentionJobsResponse;
    }) => ({
      rows: toOperationRows(data.lifecycle.items, [], t),
      auditRows: toOperationRows([], data.auditRetention, t),
      nextPageToken: data.lifecycle.nextPageToken,
      repositories: Array.from(
        new Map(
          data.lifecycle.items.map((record) => [
            record.repositoryId,
            { id: record.repositoryId, name: record.repositoryName },
          ]),
        ).values(),
      ).sort((a, b) => a.name.localeCompare(b.name)),
    }),
    [t],
  );
  const query = useQuery({
    queryKey: [...operationKeys.jobs(), { pageSize, pageToken }] as const,
    queryFn: async ({ signal }) => {
      const [lifecycle, auditRetention] = await Promise.all([
        unwrap(
          listLifecycleJobs({ query: { limit: pageSize, pageToken }, signal }),
        ),
        pageToken
          ? Promise.resolve<ListAuditRetentionJobsResponse>([])
          : unwrap(listAuditRetentionJobs({ signal })),
      ]);
      return { lifecycle, auditRetention };
    },
    select,
    placeholderData: keepPreviousData,
    staleTime: 0,
    refetchInterval: (current) => {
      const data = current.state.data;
      const active =
        data?.lifecycle.items.some((record) =>
          ACTIVE_JOB_STATES.includes(record.job.state),
        ) ||
        data?.auditRetention.some((job) =>
          ACTIVE_JOB_STATES.includes(job.state),
        );
      return poll && active ? POLL_INTERVAL : false;
    },
  });
  return {
    rows: query.data?.rows ?? null,
    auditRows: query.data?.auditRows ?? [],
    nextPageToken: query.data?.nextPageToken,
    isPlaceholderData: query.isPlaceholderData,
    repositories: query.data?.repositories ?? [],
    error: query.error,
    isFetching: query.isFetching,
    refetch: query.refetch,
  };
}

/** Run, retry or cancel one repository lifecycle job, then re-read the list. */
export function useControlJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ row, action }: { row: OperationRow; action: JobAction }) => {
      const options = {
        path: { repositoryId: row.repositoryId ?? "", lifecycleJobId: row.id },
      };
      return unwrap(
        action === "run"
          ? runRepositoryLifecycleJobNow(options)
          : action === "retry"
            ? retryRepositoryLifecycleJob(options)
            : cancelRepositoryLifecycleJob(options),
      );
    },
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: operationKeys.jobs() }),
  });
}
