import { useState } from "react";
import { PlusOutlined } from "@ant-design/icons";
import { Button, Input, Select, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  deleteServiceAccountAssertionAudience,
  putServiceAccountAssertionAudience,
  type ServiceAccount,
  type ServiceAccountAssertionAudience,
} from "../../client";
import {
  ConsoleTable,
  CopyableValue,
} from "../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  Notice,
} from "../../components/ui/Feedback";
import { CardHeader, Field } from "../../components/ui/Layout";
import { ConfirmDialog, Modal, useDisclosure } from "../../components/ui/Modal";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import {
  useAssertionAudiences,
  useInvalidateAssertionAudiences,
} from "./serviceAccountQueries";

/** The lifetimes an allow-list entry may cap an assertion at (60 to 600 seconds, ADR 0020). */
const LIFETIMES = [
  { seconds: 60, key: "assertions.lifetime1m" },
  { seconds: 120, key: "assertions.lifetime2m" },
  { seconds: 300, key: "assertions.lifetime5m" },
  { seconds: 600, key: "assertions.lifetime10m" },
] as const;

/**
 * The assertion subject and the audience allow-list of one service account (ADR 0020), a section of the account card.
 * The subject is read-only: it is stored when the account is created and never changes. Audiences are an explicit
 * act; there is no default list.
 */
export function AssertionAudiences({ account }: { account: ServiceAccount }) {
  const { locale, t } = usePreferences();
  const query = useAssertionAudiences(account.id);
  const invalidate = useInvalidateAssertionAudiences();
  const addDialog = useDisclosure();
  const [audience, setAudience] = useState("");
  const [lifetime, setLifetime] = useState<number>(600);
  const [toRemove, setToRemove] =
    useState<ServiceAccountAssertionAudience | null>(null);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);
  const [removeError, setRemoveError] = useState<unknown>(null);
  const entries = query.data?.items ?? null;
  const loadError = removeError ?? query.error ?? null;

  const close = () => {
    addDialog.hide();
    setSaveError(null);
    setAudience("");
    setLifetime(600);
  };

  const save = async () => {
    setBusy(true);
    setSaveError(null);
    try {
      const { data, error } = await putServiceAccountAssertionAudience({
        path: { serviceAccountId: account.id },
        body: { audience: audience.trim(), maxLifetimeSeconds: lifetime },
      });
      if (error || !data) {
        setSaveError(error ?? new Error(t("assertions.saveFailed")));
        return;
      }
      close();
      await invalidate(account.id);
    } catch (nextError) {
      setSaveError(nextError);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!toRemove) return;
    setBusy(true);
    setRemoveError(null);
    try {
      const { error } = await deleteServiceAccountAssertionAudience({
        path: { serviceAccountId: account.id },
        query: { audience: toRemove.audience },
      });
      if (error) {
        setRemoveError(error);
        return;
      }
      setToRemove(null);
      await invalidate(account.id);
    } catch (nextError) {
      setRemoveError(nextError);
    } finally {
      setBusy(false);
    }
  };

  const columns: ColumnsType<ServiceAccountAssertionAudience> = [
    {
      title: t("assertions.audience"),
      dataIndex: "audience",
      key: "audience",
      render: (value: string) => (
        <span className="font-medium text-fg break-all">{value}</span>
      ),
    },
    {
      title: t("assertions.maxLifetime"),
      dataIndex: "maxLifetimeSeconds",
      key: "maxLifetimeSeconds",
      width: 160,
      render: (seconds: number) => (
        <span className="text-xs text-fg-tertiary">{seconds} s</span>
      ),
    },
    {
      title: t("assertions.added"),
      dataIndex: "createdAt",
      key: "createdAt",
      width: 180,
      render: (value?: string) => (
        <span className="text-xs text-fg-tertiary">
          {formatDate(value, locale)}
        </span>
      ),
    },
    {
      key: "actions",
      align: "right",
      width: 92,
      render: (_value, entry) => (
        <Button
          danger
          type="text"
          size="small"
          aria-label={t("assertions.removeAudience", {
            audience: entry.audience,
          })}
          onClick={() => setToRemove(entry)}
        >
          {t("assertions.remove")}
        </Button>
      ),
    },
  ];

  return (
    <>
      <CardHeader
        title={t("assertions.title")}
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={account.state !== "active" || !account.assertionSubject}
            onClick={addDialog.show}
          >
            {t("assertions.allowAudience")}
          </Button>
        }
      />
      <div className="space-y-3 px-5 pb-4">
        <p className="text-xs text-fg-tertiary">
          {t("assertions.description")}
        </p>
        {account.assertionSubject ? (
          <div className="rounded-lg border border-line bg-surface-translucent px-3 py-2.5">
            <div className="text-xs font-medium text-fg-disabled">
              {t("assertions.subject")}
            </div>
            <CopyableValue
              value={account.assertionSubject}
              className="mt-1 max-w-full text-xs text-fg-secondary"
            />
            <p className="mt-2 text-xs text-fg-tertiary">
              {t("assertions.subjectHint")}
            </p>
          </div>
        ) : (
          <Notice
            tone="warning"
            closable={false}
            title={t("assertions.noSubjectTitle")}
            description={t("assertions.noSubjectDescription")}
          />
        )}
      </div>
      {loadError !== null && (
        <div className="px-5 pb-4">
          <ErrorBanner
            error={loadError}
            onRetry={() => {
              setRemoveError(null);
              void invalidate(account.id);
            }}
            tone={entries === null ? "error" : "warning"}
          />
        </div>
      )}
      {entries === null ? (
        loadError === null ? (
          <div className="p-8">
            <Loading label={t("assertions.loading")} />
          </div>
        ) : null
      ) : entries.length === 0 ? (
        <div className="p-8">
          <EmptyState
            title={t("assertions.emptyTitle")}
            hint={t("assertions.emptyHint")}
          />
        </div>
      ) : (
        <ConsoleTable<ServiceAccountAssertionAudience>
          rowKey="audience"
          columns={columns}
          dataSource={entries}
          pagination={false}
          size="small"
          scroll={{ x: 640 }}
        />
      )}

      <Modal
        open={addDialog.open}
        title={t("assertions.allowAudience")}
        onClose={close}
        busy={busy}
        footer={
          <Space>
            <Button onClick={close} disabled={busy}>
              {t("assertions.cancel")}
            </Button>
            <Button
              type="primary"
              loading={busy}
              disabled={!audience.trim()}
              onClick={() => void save()}
            >
              {t("assertions.allow")}
            </Button>
          </Space>
        }
      >
        <div className="space-y-4">
          {saveError !== null && <ErrorBanner error={saveError} />}
          <Field
            label={t("assertions.audience")}
            hint={t("assertions.audienceHint")}
          >
            <Input
              value={audience}
              maxLength={255}
              placeholder="api://AzureADTokenExchange"
              onChange={(event) => setAudience(event.target.value)}
            />
          </Field>
          <Field label={t("assertions.maxLifetime")}>
            <Select<number>
              className="w-full"
              value={lifetime}
              onChange={setLifetime}
              options={LIFETIMES.map((option) => ({
                value: option.seconds,
                label: t(option.key),
              }))}
            />
          </Field>
        </div>
      </Modal>
      <ConfirmDialog
        open={toRemove !== null}
        title={t("assertions.removeTitle")}
        message={t("assertions.removeMessage", {
          audience: toRemove?.audience ?? "",
        })}
        confirmLabel={t("assertions.remove")}
        danger
        busy={busy}
        onClose={() => setToRemove(null)}
        onConfirm={() => void remove()}
      />
    </>
  );
}
