import { useState } from "react";
import { PlusOutlined, StopOutlined } from "@ant-design/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Input, Select } from "antd";
import { createScimToken, listScimTokens, revokeScimToken } from "../../client";
import type { IdentityProvider, ScimToken } from "../../client";
import { StateBadge } from "../../components/ui/Badge";
import { ErrorBanner, Notice } from "../../components/ui/Feedback";
import { Card, Field } from "../../components/ui/Layout";
import {
  ConfirmDanger,
  FormModal,
  useDisclosure,
} from "../../components/ui/Modal";
import { OneTimeTokenDialog } from "../../components/ui/OneTimeTokenDialog";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { settingsKeys } from "../../lib/query/keys";

/** A token is flagged this many days before it expires. */
export const SCIM_TOKEN_WARNING_DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * Offered lifetimes in days. The server caps a token at 365 days from its own clock, so the longest offer stays a day
 * short of it and a browser clock that runs ahead cannot make the request fail.
 */
type ValidityDays = 30 | 90 | 180 | 364;

/** Whole days until a token expires; negative once it has. */
export function daysUntilExpiry(
  expiresAt: string,
  now: number = Date.now(),
): number {
  return Math.ceil((Date.parse(expiresAt) - now) / DAY_MS);
}

/** Active tokens that expire within the warning window, soonest first. */
export function expiringTokens(
  tokens: ScimToken[],
  now: number = Date.now(),
): ScimToken[] {
  return tokens
    .filter(
      (token) =>
        token.state !== "revoked" &&
        daysUntilExpiry(token.expiresAt, now) <= SCIM_TOKEN_WARNING_DAYS,
    )
    .sort((a, b) => Date.parse(a.expiresAt) - Date.parse(b.expiresAt));
}

/**
 * The tokens of one provider. The list never carries a secret, so the query cache holds none; the plaintext of
 * a new token lives in the reveal dialog's state only.
 */
function useScimTokens(providerId: string) {
  return useQuery({
    queryKey: settingsKeys.scimTokens(providerId),
    queryFn: async ({ signal }) =>
      (await unwrap(listScimTokens({ path: { providerId }, signal })))?.items ??
      [],
    staleTime: 0,
  });
}

function CreateTokenDialog({
  provider,
  onCreated,
}: {
  provider: IdentityProvider;
  onCreated: (token: string) => void;
}) {
  const { t } = usePreferences();
  const queryClient = useQueryClient();
  const dialog = useDisclosure();
  const [name, setName] = useState("");
  const [validDays, setValidDays] = useState<ValidityDays>(90);

  const close = () => {
    dialog.hide();
    setName("");
    setValidDays(90);
  };

  const submit = async () => {
    // Called directly, not through a mutation: a mutation keeps its result, plaintext included, in the
    // mutation cache for minutes after the dialog closes.
    const created = await unwrap(
      createScimToken({
        path: { providerId: provider.id },
        body: {
          name: name.trim(),
          expiresAt: new Date(Date.now() + validDays * DAY_MS).toISOString(),
        },
      }),
    );
    if (!created) return;
    const { token } = created;
    close();
    await queryClient.invalidateQueries({
      queryKey: settingsKeys.scimTokens(provider.id),
    });
    onCreated(token);
  };

  return (
    <>
      <Button icon={<PlusOutlined />} onClick={dialog.show}>
        {t("scim.newToken")}
      </Button>
      <FormModal
        open={dialog.open}
        title={t("scim.newTokenFor", { name: provider.displayName })}
        submitLabel={t("scim.create")}
        canSubmit={!!name.trim()}
        onSubmit={submit}
        onClose={close}
      >
        <Field label={t("scim.tokenName")} hint={t("scim.tokenNameHint")}>
          <Input
            placeholder="entra-provisioning"
            maxLength={128}
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("scim.validity")} hint={t("scim.validityHint")}>
          <Select<ValidityDays>
            className="w-full"
            value={validDays}
            onChange={setValidDays}
            options={[
              { value: 30, label: t("scim.days30") },
              { value: 90, label: t("scim.days90") },
              { value: 180, label: t("scim.days180") },
              { value: 364, label: t("scim.year1") },
            ]}
          />
        </Field>
        <p className="text-xs text-fg-tertiary">{t("scim.shownOnce")}</p>
      </FormModal>
    </>
  );
}

function ProviderTokens({
  provider,
  onReveal,
}: {
  provider: IdentityProvider;
  onReveal: (provider: IdentityProvider, token: string) => void;
}) {
  const { locale, t } = usePreferences();
  const queryClient = useQueryClient();
  const query = useScimTokens(provider.id);
  const [toRevoke, setToRevoke] = useState<ScimToken | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const tokens = query.data ?? [];
  const expiring = expiringTokens(tokens);
  const failure = actionError ?? query.error;

  return (
    <section className="space-y-3" aria-label={provider.displayName}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-medium">
          {t("scim.forProvider", { name: provider.displayName })}
        </h3>
        <CreateTokenDialog
          provider={provider}
          onCreated={(token) => onReveal(provider, token)}
        />
      </div>
      {failure != null && <ErrorBanner error={failure} />}
      {expiring.map((token) => {
        const days = daysUntilExpiry(token.expiresAt);
        const date = formatDate(token.expiresAt, locale);
        // A token that lapsed less than a day ago rounds to 0 days, so the instant decides, not the day count.
        const lapsed =
          token.state === "expired" ||
          Date.parse(token.expiresAt) <= Date.now();
        return (
          <Notice
            key={token.id}
            tone="warning"
            closable={false}
            title={
              lapsed
                ? t("scim.tokenExpired", { name: token.name, date })
                : t("scim.tokenExpiresSoon", { name: token.name, days, date })
            }
          />
        );
      })}
      {query.isSuccess && tokens.length === 0 ? (
        <p className="text-sm text-muted">{t("scim.noTokens")}</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-fg-tertiary">
              <th scope="col">{t("scim.name")}</th>
              <th scope="col">{t("scim.status")}</th>
              <th scope="col">{t("scim.created")}</th>
              <th scope="col">{t("scim.expires")}</th>
              <th scope="col">
                <span className="sr-only">{t("scim.actions")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {tokens.map((token) => (
              <tr key={token.id}>
                <td className="font-medium">{token.name}</td>
                <td>
                  <StateBadge state={token.state} subject="identity" />
                </td>
                <td className="whitespace-nowrap text-xs text-fg-tertiary">
                  {formatDate(token.createdAt, locale)}
                </td>
                <td className="whitespace-nowrap text-xs text-fg-tertiary">
                  {formatDate(token.expiresAt, locale)}
                </td>
                <td className="text-right">
                  {token.state !== "revoked" && (
                    <Button
                      size="small"
                      danger
                      icon={<StopOutlined />}
                      aria-label={t("scim.revokeToken", { name: token.name })}
                      onClick={() => {
                        setActionError(null);
                        setToRevoke(token);
                      }}
                    >
                      {t("scim.revoke")}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <ConfirmDanger
        target={toRevoke}
        title={t("scim.revokeTitle")}
        message={(token) =>
          t("scim.revokeMessage", {
            name: token.name,
            provider: provider.displayName,
          })
        }
        confirmLabel={t("scim.revoke")}
        onClose={() => setToRevoke(null)}
        onConfirm={async (token) => {
          await unwrap(
            revokeScimToken({
              path: { providerId: provider.id, tokenId: token.id },
            }),
          );
          await queryClient.invalidateQueries({
            queryKey: settingsKeys.scimTokens(provider.id),
          });
        }}
        onError={setActionError}
      />
    </section>
  );
}

/**
 * SCIM tokens per identity provider (ADR 0018 section 4): create one, see it once, watch its expiry and revoke it.
 * The plaintext is returned by the create call only; it is held in this component's state while the reveal dialog
 * is open and dropped when the dialog closes, never in the query cache or in storage.
 */
export function ScimTokensCard({
  providers,
}: {
  providers: IdentityProvider[];
}) {
  const { t } = usePreferences();
  const [revealed, setRevealed] = useState<{
    provider: IdentityProvider;
    token: string;
  } | null>(null);
  if (providers.length === 0) return null;
  return (
    <Card>
      <div className="space-y-4">
        <div>
          <h2 className="text-base font-semibold">{t("scim.title")}</h2>
          <p className="text-sm text-muted">{t("scim.description")}</p>
        </div>
        {providers.map((provider) => (
          <ProviderTokens
            key={provider.id}
            provider={provider}
            onReveal={(owner, token) => setRevealed({ provider: owner, token })}
          />
        ))}
      </div>
      {revealed && (
        <OneTimeTokenDialog
          title={t("scim.tokenCreated")}
          warningTitle={t("scim.copyNow")}
          warningDescription={t("scim.copyNowDescription")}
          token={revealed.token}
          details={t("scim.baseUrlHint", {
            provider: revealed.provider.displayName,
          })}
          doneLabel={t("scim.done")}
          onDone={() => setRevealed(null)}
        />
      )}
    </Card>
  );
}
