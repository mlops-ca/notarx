import { useState } from "react";
import { PlusOutlined } from "@ant-design/icons";
import { Button, Space, Switch } from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  deleteIdentityProvider,
  replaceIdentityProvider,
  rotateIdentityProviderSpKey,
  testIdentityProvider,
  type IdentityProvider,
} from "../../client";
import { Badge } from "../../components/ui/Badge";
import { ConsoleTable } from "../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  Notice,
} from "../../components/ui/Feedback";
import { Card, PageHeader } from "../../components/ui/Layout";
import { ConfirmDanger, useDisclosure } from "../../components/ui/Modal";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { settingsKeys, identityKeys } from "../../lib/query/keys";
import { useQueryClient } from "@tanstack/react-query";
import { AuthorizationCacheCard } from "./AuthorizationCache";
import {
  ProviderWarnings,
  providerWarningCount,
} from "./providers/ProviderWarnings";
import { ProviderWizard } from "./providers/ProviderWizard";
import { draftFromProvider, toReplaceBody } from "./providers/providerModel";
import {
  useGroupsOverage,
  useIdentityProviders,
} from "./providers/providerQueries";
import "./settings.css";

function SamlDetails({ provider }: { provider: IdentityProvider }) {
  const { locale, t } = usePreferences();
  const saml = provider.saml;
  if (!saml) return null;
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
      <dt className="text-fg-tertiary">{t("providers.spMetadataUrl")}</dt>
      <dd>
        <code>{saml.metadataEndpoint || "-"}</code>
      </dd>
      <dt className="text-fg-tertiary">{t("providers.acsUrl")}</dt>
      <dd>
        <code>{saml.acsUrl || "-"}</code>
      </dd>
      <dt className="text-fg-tertiary">{t("providers.certificates")}</dt>
      <dd>
        {saml.certificates.length === 0 ? (
          t("providers.noCertificates")
        ) : (
          <ul>
            {saml.certificates.map((certificate) => (
              <li key={certificate.pem}>
                {certificate.state === "next"
                  ? t("providers.certNext")
                  : t("providers.certActive")}
                {" - "}
                {t("providers.certExpires", {
                  date: formatDate(certificate.notAfter, locale),
                })}
              </li>
            ))}
          </ul>
        )}
      </dd>
    </dl>
  );
}

export function IdentityProvidersPage() {
  const { t } = usePreferences();
  const queryClient = useQueryClient();
  const providersQuery = useIdentityProviders();
  const overageQuery = useGroupsOverage();
  const wizard = useDisclosure();
  const [editing, setEditing] = useState<IdentityProvider | null>(null);
  const [toDelete, setToDelete] = useState<IdentityProvider | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const [notice, setNotice] = useState<{
    tone: "success" | "warning";
    text: string;
  } | null>(null);
  const [busyId, setBusyId] = useState("");
  const providers = providersQuery.data ?? null;
  const overage = overageQuery.data ?? new Map<string, string>();
  const error = actionError ?? providersQuery.error ?? null;

  const refresh = async () => {
    // Provider changes also change what the sign-in page and the link form offer.
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: settingsKeys.all }),
      queryClient.invalidateQueries({ queryKey: identityKeys.linkProvider() }),
      queryClient.invalidateQueries({
        queryKey: ["auth", "identity-providers"],
      }),
    ]);
  };
  const run = async (
    provider: IdentityProvider,
    action: () => Promise<string | void>,
  ) => {
    setBusyId(provider.id);
    setActionError(null);
    setNotice(null);
    try {
      const text = await action();
      if (text) setNotice({ tone: "success", text });
    } catch (nextError) {
      setActionError(nextError);
    } finally {
      setBusyId("");
    }
    await refresh();
  };
  const toggle = (provider: IdentityProvider, enabled: boolean) =>
    run(provider, async () => {
      await unwrap(
        replaceIdentityProvider({
          path: { providerId: provider.id },
          headers: { "If-Match": provider.version },
          body: toReplaceBody(
            { ...draftFromProvider(provider), enabled },
            provider,
          ),
        }),
      );
    });
  const test = (provider: IdentityProvider) =>
    run(provider, async () => {
      const result = await unwrap(
        testIdentityProvider({ path: { providerId: provider.id } }),
      );
      return t("providers.testOk", { latencyMs: result?.latencyMs ?? 0 });
    });
  const rotate = (provider: IdentityProvider) =>
    run(provider, async () => {
      await unwrap(
        rotateIdentityProviderSpKey({ path: { providerId: provider.id } }),
      );
      return t("providers.rotated", { name: provider.displayName });
    });

  const columns: ColumnsType<IdentityProvider> = [
    {
      title: t("providers.name"),
      key: "name",
      render: (_, provider) => (
        <div>
          <div className="font-medium">{provider.displayName}</div>
          <code className="text-xs text-fg-tertiary">{provider.slug}</code>
        </div>
      ),
    },
    {
      title: t("providers.protocol"),
      key: "protocol",
      render: (_, provider) => (
        <Badge tone="visualization-1">
          {t(`providers.protocol.${provider.kind}`)}
        </Badge>
      ),
    },
    {
      title: t("providers.status"),
      key: "status",
      render: (_, provider) => (
        <Switch
          checked={provider.enabled}
          loading={busyId === provider.id}
          checkedChildren={t("providers.enabled")}
          unCheckedChildren={t("providers.disabled")}
          aria-label={`${provider.displayName} ${provider.enabled ? t("providers.enabled") : t("providers.disabled")}`}
          onChange={(enabled) => void toggle(provider, enabled)}
        />
      ),
    },
    {
      title: t("providers.provisioning"),
      key: "provisioning",
      render: (_, provider) => t(`providers.mode.${provider.provisioningMode}`),
    },
    {
      title: t("providers.warnings"),
      key: "warnings",
      render: (_, provider) => {
        const count = providerWarningCount(provider, overage);
        return count === 0 ? (
          <span className="text-fg-tertiary">{t("providers.none")}</span>
        ) : (
          <Badge tone="warning">{count}</Badge>
        );
      },
    },
    {
      title: t("providers.actions"),
      key: "actions",
      render: (_, provider) => (
        <Space wrap>
          <Button
            size="small"
            onClick={() => {
              setEditing(provider);
              wizard.show();
            }}
          >
            {t("providers.edit")}
          </Button>
          {provider.kind === "oidc" && (
            <Button size="small" onClick={() => void test(provider)}>
              {t("providers.test")}
            </Button>
          )}
          {provider.kind === "saml" && provider.saml?.signRequests && (
            <Button size="small" onClick={() => void rotate(provider)}>
              {t("providers.rotateKey")}
            </Button>
          )}
          <Button size="small" danger onClick={() => setToDelete(provider)}>
            {t("providers.delete")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className="ag-page-stack">
      <PageHeader
        title={t("providers.title")}
        description={t("providers.description")}
        actions={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null);
              wizard.show();
            }}
          >
            {t("providers.add")}
          </Button>
        }
      />
      {error !== null && (
        <ErrorBanner
          error={error}
          onRetry={() => void providersQuery.refetch()}
        />
      )}
      {notice && (
        <Notice
          tone={notice.tone}
          title={notice.text}
          onClose={() => setNotice(null)}
        />
      )}
      <Notice
        tone="info"
        closable={false}
        title={t("providers.sloTitle")}
        description={t("providers.slo")}
      />
      {providers && (
        <ProviderWarnings providers={providers} overage={overage} />
      )}
      <Card>
        {providers === null ? (
          <div className="p-8">
            <Loading label={t("providers.loading")} />
          </div>
        ) : providers.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={t("providers.empty")}
              hint={t("providers.emptyHint")}
            />
          </div>
        ) : (
          <ConsoleTable<IdentityProvider>
            rowKey="id"
            dataSource={providers}
            columns={columns}
            pagination={false}
            scroll={{ x: 900 }}
            expandable={{
              rowExpandable: (provider) => provider.kind === "saml",
              expandedRowRender: (provider) => (
                <SamlDetails provider={provider} />
              ),
            }}
          />
        )}
      </Card>
      <AuthorizationCacheCard />
      <ProviderWizard
        key={editing?.id ?? "new"}
        open={wizard.open}
        provider={editing}
        onClose={wizard.hide}
        onDone={async (text, warning) => {
          wizard.hide();
          setNotice(
            warning
              ? { tone: "warning", text: warning }
              : { tone: "success", text },
          );
          await refresh();
        }}
      />
      <ConfirmDanger
        target={toDelete}
        title={t("providers.deleteTitle")}
        message={(provider) =>
          t("providers.deleteMessage", { name: provider.displayName })
        }
        confirmLabel={t("providers.delete")}
        onConfirm={(provider) =>
          unwrap(
            deleteIdentityProvider({
              path: { providerId: provider.id },
              headers: { "If-Match": provider.version },
            }),
          ).then(() => {})
        }
        onClose={() => setToDelete(null)}
        onDone={() => void refresh()}
        onError={setActionError}
      />
    </div>
  );
}
