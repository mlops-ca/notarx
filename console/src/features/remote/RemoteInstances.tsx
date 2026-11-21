import { useState } from "react";
import { PlusOutlined } from "@ant-design/icons";
import { Button, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  deleteRemoteInstance,
  testRemoteInstance,
  type RemoteInstance,
  type RemoteInstanceTestResult,
} from "../../client";
import { StateBadge, StatusText } from "../../components/ui/Badge";
import { ConsoleTable } from "../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  Notice,
} from "../../components/ui/Feedback";
import { Card, PageHeader, Pagination } from "../../components/ui/Layout";
import { ConfirmDanger } from "../../components/ui/Modal";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { remoteInstanceInUse } from "../../lib/remoteInstanceInUse";
import { RemoteInstanceDialog } from "./RemoteInstanceDialog";
import {
  useInvalidateRemoteInstances,
  useRemoteInstancePages,
} from "./remoteInstanceQueries";

/** Platform administrators register, test, edit and delete the remote notarx instances promotion can target. */
export function RemoteInstancesPage() {
  const { locale, t } = usePreferences();
  const query = useRemoteInstancePages();
  const invalidate = useInvalidateRemoteInstances();
  const [dialog, setDialog] = useState<{
    instance: RemoteInstance | null;
  } | null>(null);
  const [toDelete, setToDelete] = useState<RemoteInstance | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [result, setResult] = useState<{
    name: string;
    value: RemoteInstanceTestResult;
  } | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const items = query.data?.pages.flatMap((page) => page.items) ?? null;
  const error = actionError ?? query.error ?? null;

  const runTest = async (instance: RemoteInstance) => {
    setActionError(null);
    setTesting(instance.id);
    try {
      const value = await unwrap(
        testRemoteInstance({ path: { remoteInstanceId: instance.id } }),
      );
      setResult({ name: instance.name, value });
      await invalidate();
    } catch (nextError) {
      setActionError(nextError);
    } finally {
      setTesting(null);
    }
  };

  const columns: ColumnsType<RemoteInstance> = [
    {
      title: t("remoteInstances.name"),
      dataIndex: "name",
      key: "name",
      render: (value: string) => (
        <span className="font-medium text-fg">{value}</span>
      ),
    },
    {
      title: t("remoteInstances.baseUrl"),
      dataIndex: "baseUrl",
      key: "baseUrl",
      render: (value: string) => (
        <span className="font-mono text-xs break-all">{value}</span>
      ),
    },
    {
      title: t("remoteInstances.instanceId"),
      dataIndex: "instanceId",
      key: "instanceId",
      render: (value: string) => (
        <span className="font-mono text-xs break-all">{value}</span>
      ),
    },
    {
      title: t("remoteInstances.auth"),
      key: "auth",
      render: (_, instance) => (
        <div className="text-xs">
          <div>{t(`remoteInstances.auth.${instance.authKind}`)}</div>
          {instance.authKind === "static_token" && (
            <div className="text-fg-tertiary">
              {instance.tokenConfigured
                ? t("remoteInstances.tokenConfigured")
                : t("remoteInstances.tokenNotConfigured")}
            </div>
          )}
        </div>
      ),
    },
    {
      title: t("remoteInstances.state"),
      key: "state",
      render: (_, instance) =>
        instance.state === "unreachable" ? (
          <StatusText tone="danger">
            {t("remoteInstances.stateUnreachable")}
          </StatusText>
        ) : (
          <StateBadge state={instance.state} />
        ),
    },
    {
      title: t("remoteInstances.lastChecked"),
      key: "lastCheckedAt",
      render: (_, instance) => (
        <span className="text-xs text-fg-tertiary">
          {formatDate(instance.lastCheckedAt, locale)}
        </span>
      ),
    },
    {
      title: t("remoteInstances.actions"),
      key: "actions",
      align: "right",
      render: (_, instance) => (
        <Space size="small">
          <Button
            type="text"
            size="small"
            loading={testing === instance.id}
            aria-label={t("remoteInstances.testRow", { name: instance.name })}
            onClick={() => void runTest(instance)}
          >
            {t("remoteInstances.test")}
          </Button>
          <Button
            type="text"
            size="small"
            aria-label={t("remoteInstances.editRow", { name: instance.name })}
            onClick={() => setDialog({ instance })}
          >
            {t("remoteInstances.editAction")}
          </Button>
          <Button
            danger
            type="text"
            size="small"
            aria-label={t("remoteInstances.deleteRow", { name: instance.name })}
            onClick={() => setToDelete(instance)}
          >
            {t("remoteInstances.delete")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className="space-y-5">
      <PageHeader
        title={t("remoteInstances.title")}
        description={t("remoteInstances.description")}
        actions={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setDialog({ instance: null })}
          >
            {t("remoteInstances.add")}
          </Button>
        }
      />
      {error !== null && (
        <ErrorBanner
          error={error}
          onRetry={() => {
            setActionError(null);
            void invalidate();
          }}
          tone={items === null ? "error" : "warning"}
        />
      )}
      {result && (
        <Notice
          tone={result.value.reachable ? "success" : "warning"}
          title={t(
            result.value.reachable
              ? "remoteInstances.testReachable"
              : "remoteInstances.testUnreachable",
            { name: result.name },
          )}
          description={
            result.value.reachable
              ? t("remoteInstances.testDetail", {
                  instanceId: result.value.instanceId ?? "",
                  apiVersion: result.value.apiVersion ?? "",
                  formats: result.value.formats.join(", "),
                })
              : t("remoteInstances.testFailed", {
                  error: result.value.error ?? "",
                })
          }
          onClose={() => setResult(null)}
        />
      )}
      <Card>
        {items === null ? (
          error === null && (
            <div className="p-8">
              <Loading label={t("remoteInstances.loading")} />
            </div>
          )
        ) : items.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={t("remoteInstances.emptyTitle")}
              hint={t("remoteInstances.emptyHint")}
            />
          </div>
        ) : (
          <ConsoleTable<RemoteInstance>
            rowKey="id"
            dataSource={items}
            columns={columns}
            pagination={false}
            scroll={{ x: 900 }}
          />
        )}
        <Pagination
          hasMore={Boolean(query.hasNextPage)}
          loading={query.isFetchingNextPage}
          onMore={() => void query.fetchNextPage()}
          label={t("remoteInstances.loadMore")}
        />
      </Card>
      <RemoteInstanceDialog
        open={dialog !== null}
        instance={dialog?.instance ?? null}
        onClose={() => setDialog(null)}
        onSaved={async () => {
          setDialog(null);
          await invalidate();
        }}
      />
      <ConfirmDanger
        target={toDelete}
        title={t("remoteInstances.deleteTitle")}
        message={(instance) =>
          t("remoteInstances.deleteMessage", { name: instance.name })
        }
        confirmLabel={t("remoteInstances.delete")}
        onConfirm={(instance) =>
          unwrap(
            deleteRemoteInstance({ path: { remoteInstanceId: instance.id } }),
          ).then(
            () => {},
            (error: unknown) => {
              throw remoteInstanceInUse(error, instance.name);
            },
          )
        }
        onClose={() => setToDelete(null)}
        onDone={() => void invalidate()}
      />
    </div>
  );
}
