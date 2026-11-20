import { useState } from "react";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { Button, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  deleteWorkloadIssuer,
  deleteWorkloadTrustPolicy,
  listServiceAccounts,
  probeWorkloadIssuer,
  updateWorkloadIssuer,
  updateWorkloadTrustPolicy,
  type WorkloadIssuer,
  type WorkloadIssuerProbe,
  type WorkloadTrustPolicy,
} from "../../client";
import { useQuery } from "@tanstack/react-query";
import { StateBadge } from "../../components/ui/Badge";
import { ConsoleTable } from "../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  Notice,
} from "../../components/ui/Feedback";
import {
  Card,
  CardHeader,
  PageHeader,
  Pagination,
} from "../../components/ui/Layout";
import { ConfirmDanger, useDisclosure } from "../../components/ui/Modal";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { workloadKeys } from "../../lib/query/keys";
import { IssuerDialog, PolicyDialog, ProbeDialog } from "./WorkloadDialogs";
import { PolicyTester } from "./PolicyTester";
import {
  useInvalidateWorkload,
  useWorkloadIssuerPages,
  useWorkloadPolicyPages,
} from "./workloadQueries";

/** Service accounts a policy can map to: one page, as the access pages do. */
function useAccountChoices() {
  return useQuery({
    queryKey: [...workloadKeys.all, "service-accounts"],
    queryFn: ({ signal }) =>
      unwrap(listServiceAccounts({ query: { pageSize: 200 }, signal })),
  });
}

export function WorkloadIdentityPage() {
  const { locale, t } = usePreferences();
  const issuersQuery = useWorkloadIssuerPages();
  const policiesQuery = useWorkloadPolicyPages();
  const accountsQuery = useAccountChoices();
  const invalidate = useInvalidateWorkload();
  const issuerDialog = useDisclosure();
  const policyDialog = useDisclosure();
  const issuers: WorkloadIssuer[] | null =
    issuersQuery.data?.pages.flatMap((page) => page.items) ?? null;
  const policies: WorkloadTrustPolicy[] | null =
    policiesQuery.data?.pages.flatMap((page) => page.items) ?? null;
  const accounts = accountsQuery.data?.items ?? [];
  const [actionError, setActionError] = useState<unknown>(null);
  const [probe, setProbe] = useState<{
    issuer: WorkloadIssuer;
    result: WorkloadIssuerProbe;
  } | null>(null);
  const [issuerToDelete, setIssuerToDelete] = useState<WorkloadIssuer | null>(
    null,
  );
  const [issuerToDisable, setIssuerToDisable] = useState<WorkloadIssuer | null>(
    null,
  );
  const [policyToDelete, setPolicyToDelete] =
    useState<WorkloadTrustPolicy | null>(null);
  const [policyToDisable, setPolicyToDisable] =
    useState<WorkloadTrustPolicy | null>(null);
  const error =
    actionError ?? issuersQuery.error ?? policiesQuery.error ?? null;

  const refresh = () => {
    setActionError(null);
    void invalidate();
  };
  const run = async (action: () => Promise<{ error?: unknown }>) => {
    setActionError(null);
    try {
      const { error: failure } = await action();
      if (failure) setActionError(failure);
    } catch (nextError) {
      setActionError(nextError);
    }
    await invalidate();
  };
  const runProbe = async (issuer: WorkloadIssuer) => {
    setActionError(null);
    try {
      const { data, error: failure } = await probeWorkloadIssuer({
        body: { issuerId: issuer.id },
      });
      if (failure || !data) {
        setActionError(failure);
        return;
      }
      setProbe({ issuer, result: data });
    } catch (nextError) {
      setActionError(nextError);
    }
  };

  const issuerName = (id: string) =>
    issuers?.find((issuer) => issuer.id === id)?.name ?? id;
  const accountName = (id: string) =>
    accounts.find((account) => account.id === id)?.name ?? id;

  const issuerColumns: ColumnsType<WorkloadIssuer> = [
    { title: t("workload.name"), dataIndex: "name", key: "name" },
    {
      title: t("workload.kind"),
      key: "kind",
      render: (_, issuer) => t(`workload.kind.${issuer.kind}`),
    },
    {
      title: t("workload.issuerUrl"),
      dataIndex: "issuer",
      key: "issuer",
      render: (value: string) => (
        <span className="font-mono text-xs">{value}</span>
      ),
    },
    {
      title: t("workload.state"),
      key: "state",
      render: (_, issuer) => (
        <StateBadge state={issuer.state} subject="identity" />
      ),
    },
    {
      title: t("workload.actions"),
      key: "actions",
      render: (_, issuer) => (
        <Space wrap>
          <Button size="small" onClick={() => void runProbe(issuer)}>
            {t("workload.probe")}
          </Button>
          {issuer.state === "active" ? (
            <Button
              size="small"
              danger
              onClick={() => setIssuerToDisable(issuer)}
            >
              {t("workload.disable")}
            </Button>
          ) : (
            <Button
              size="small"
              onClick={() =>
                void run(() =>
                  updateWorkloadIssuer({
                    path: { issuerId: issuer.id },
                    headers: { "If-Match": issuer.version },
                    body: { state: "active" },
                  }),
                )
              }
            >
              {t("workload.enable")}
            </Button>
          )}
          <Button size="small" danger onClick={() => setIssuerToDelete(issuer)}>
            {t("workload.delete")}
          </Button>
        </Space>
      ),
    },
  ];

  const policyColumns: ColumnsType<WorkloadTrustPolicy> = [
    { title: t("workload.name"), dataIndex: "name", key: "name" },
    {
      title: t("workload.issuer"),
      key: "issuer",
      render: (_, policy) => issuerName(policy.issuerId),
    },
    {
      title: t("workload.serviceAccount"),
      key: "account",
      render: (_, policy) => accountName(policy.serviceAccountId),
    },
    {
      title: t("workload.conditions"),
      key: "conditions",
      render: (_, policy) =>
        t("workload.conditionCount", {
          count: String(policy.conditions.length),
        }),
    },
    {
      title: t("workload.singleUse"),
      key: "singleUse",
      render: (_, policy) =>
        policy.singleUse ? t("workload.yes") : t("workload.no"),
    },
    {
      title: t("workload.tokenLifetime"),
      key: "ttl",
      render: (_, policy) =>
        t("workload.seconds", { count: String(policy.maxTokenTtlSeconds) }),
    },
    {
      title: t("workload.expires"),
      key: "expires",
      render: (_, policy) =>
        policy.expiresAt
          ? formatDate(policy.expiresAt, locale)
          : t("workload.never"),
    },
    {
      title: t("workload.state"),
      key: "state",
      render: (_, policy) => (
        <StateBadge state={policy.state} subject="identity" />
      ),
    },
    {
      title: t("workload.actions"),
      key: "actions",
      render: (_, policy) => (
        <Space wrap>
          {policy.state === "active" ? (
            <Button
              size="small"
              danger
              onClick={() => setPolicyToDisable(policy)}
            >
              {t("workload.disable")}
            </Button>
          ) : (
            <Button
              size="small"
              onClick={() =>
                void run(() =>
                  updateWorkloadTrustPolicy({
                    path: { policyId: policy.id },
                    headers: { "If-Match": policy.version },
                    body: { state: "active" },
                  }),
                )
              }
            >
              {t("workload.enable")}
            </Button>
          )}
          <Button size="small" danger onClick={() => setPolicyToDelete(policy)}>
            {t("workload.delete")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className="ag-page-stack">
      <PageHeader
        title={t("workload.title")}
        description={t("workload.description")}
        actions={
          <>
            <Button icon={<ReloadOutlined />} onClick={refresh}>
              {t("workload.refresh")}
            </Button>
            <Button icon={<PlusOutlined />} onClick={issuerDialog.show}>
              {t("workload.newIssuer")}
            </Button>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              disabled={!issuers || issuers.length === 0}
              onClick={policyDialog.show}
            >
              {t("workload.newPolicy")}
            </Button>
          </>
        }
      />
      <Notice
        tone="info"
        closable={false}
        title={t("workload.disabledNoticeTitle")}
        description={t("workload.disabledNotice")}
      />
      {error !== null && <ErrorBanner error={error} onRetry={refresh} />}

      <Card>
        <CardHeader title={t("workload.issuers")} />
        <p className="px-5 pt-3 text-sm text-fg-tertiary">
          {t("workload.issuersHint")}
        </p>
        {issuers === null ? (
          <div className="p-8">
            <Loading label={t("workload.loadingIssuers")} />
          </div>
        ) : issuers.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={t("workload.noIssuers")}
              hint={t("workload.noIssuersHint")}
            />
          </div>
        ) : (
          <ConsoleTable<WorkloadIssuer>
            rowKey="id"
            dataSource={issuers}
            columns={issuerColumns}
            pagination={false}
            scroll={{ x: 900 }}
          />
        )}
        <Pagination
          hasMore={Boolean(issuersQuery.hasNextPage)}
          loading={issuersQuery.isFetchingNextPage}
          onMore={() => void issuersQuery.fetchNextPage()}
          label={t("workload.loadMore")}
        />
      </Card>

      <Card>
        <CardHeader title={t("workload.policies")} />
        <p className="px-5 pt-3 text-sm text-fg-tertiary">
          {t("workload.policiesHint")}
        </p>
        {policies === null ? (
          <div className="p-8">
            <Loading label={t("workload.loadingPolicies")} />
          </div>
        ) : policies.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={t("workload.noPolicies")}
              hint={t("workload.noPoliciesHint")}
            />
          </div>
        ) : (
          <ConsoleTable<WorkloadTrustPolicy>
            rowKey="id"
            dataSource={policies}
            columns={policyColumns}
            pagination={false}
            scroll={{ x: 1100 }}
          />
        )}
        <Pagination
          hasMore={Boolean(policiesQuery.hasNextPage)}
          loading={policiesQuery.isFetchingNextPage}
          onMore={() => void policiesQuery.fetchNextPage()}
          label={t("workload.loadMore")}
        />
      </Card>

      <PolicyTester issuers={issuers ?? []} />

      <IssuerDialog
        open={issuerDialog.open}
        onClose={issuerDialog.hide}
        onCreated={async () => {
          issuerDialog.hide();
          await invalidate();
        }}
      />
      <PolicyDialog
        open={policyDialog.open}
        issuers={issuers ?? []}
        accounts={accounts}
        onClose={policyDialog.hide}
        onCreated={async () => {
          policyDialog.hide();
          await invalidate();
        }}
      />
      <ProbeDialog
        probe={probe?.result ?? null}
        issuerName={probe?.issuer.name ?? ""}
        onClose={() => setProbe(null)}
      />
      <ConfirmDanger
        target={issuerToDisable}
        title={t("workload.disableIssuerTitle")}
        message={(issuer) =>
          t("workload.disableIssuerMessage", { name: issuer.name })
        }
        confirmLabel={t("workload.disable")}
        onConfirm={(issuer) =>
          unwrap(
            updateWorkloadIssuer({
              path: { issuerId: issuer.id },
              headers: { "If-Match": issuer.version },
              body: { state: "disabled" },
            }),
          ).then(() => {})
        }
        onClose={() => setIssuerToDisable(null)}
        onDone={refresh}
        onError={setActionError}
      />
      <ConfirmDanger
        target={issuerToDelete}
        title={t("workload.deleteIssuerTitle")}
        message={(issuer) =>
          t("workload.deleteIssuerMessage", { name: issuer.name })
        }
        confirmLabel={t("workload.delete")}
        onConfirm={(issuer) =>
          unwrap(
            deleteWorkloadIssuer({
              path: { issuerId: issuer.id },
              headers: { "If-Match": issuer.version },
            }),
          ).then(() => {})
        }
        onClose={() => setIssuerToDelete(null)}
        onDone={refresh}
        onError={setActionError}
      />
      <ConfirmDanger
        target={policyToDisable}
        title={t("workload.disablePolicyTitle")}
        message={(policy) =>
          t("workload.disablePolicyMessage", { name: policy.name })
        }
        confirmLabel={t("workload.disable")}
        onConfirm={(policy) =>
          unwrap(
            updateWorkloadTrustPolicy({
              path: { policyId: policy.id },
              headers: { "If-Match": policy.version },
              body: { state: "disabled" },
            }),
          ).then(() => {})
        }
        onClose={() => setPolicyToDisable(null)}
        onDone={refresh}
        onError={setActionError}
      />
      <ConfirmDanger
        target={policyToDelete}
        title={t("workload.deletePolicyTitle")}
        message={(policy) =>
          t("workload.deletePolicyMessage", { name: policy.name })
        }
        confirmLabel={t("workload.delete")}
        onConfirm={(policy) =>
          unwrap(
            deleteWorkloadTrustPolicy({
              path: { policyId: policy.id },
              headers: { "If-Match": policy.version },
            }),
          ).then(() => {})
        }
        onClose={() => setPolicyToDelete(null)}
        onDone={refresh}
        onError={setActionError}
      />
    </div>
  );
}
