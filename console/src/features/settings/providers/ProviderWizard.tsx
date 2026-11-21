import { useState } from "react";
import { Button, Input, Radio, Select, Space, Steps, Switch } from "antd";
import {
  createIdentityProvider,
  importIdentityProviderMetadata,
  replaceIdentityProvider,
  type IdentityProvider,
} from "../../../client";
import { ErrorBanner } from "../../../components/ui/Feedback";
import { Field } from "../../../components/ui/Layout";
import { Modal } from "../../../components/ui/Modal";
import { usePreferences } from "../../../lib/preferences";
import { unwrap } from "../../../lib/query/client";
import {
  PROVIDER_TEMPLATES,
  connectionErrors,
  draftFromProvider,
  issuerHintKey,
  metadataHintKey,
  newDraft,
  redirectUrlFor,
  toCreateBody,
  toReplaceBody,
  type ProviderDraft,
  type ProviderKind,
  type ProviderTemplate,
  type ProvisioningMode,
} from "./providerModel";

const STEP_KIND = 0;
const STEP_CONNECTION = 1;
const STEP_POLICY = 2;

/**
 * Creates a provider in three steps (protocol and template, connection,
 * sign-in policy) or edits one in the last two. The dialog owns its form,
 * busy state and error; the page hears only about success.
 */
export function ProviderWizard({
  open,
  provider,
  onClose,
  onDone,
}: {
  open: boolean;
  /** The provider being edited; null creates one. */
  provider: IdentityProvider | null;
  onClose: () => void;
  onDone: (message: string, warning?: string) => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const editing = provider !== null;
  const origin = window.location.origin;
  const [step, setStep] = useState(editing ? STEP_CONNECTION : STEP_KIND);
  const [draft, setDraft] = useState<ProviderDraft>(() =>
    provider ? draftFromProvider(provider) : newDraft("oidc", "entra", origin),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [showErrors, setShowErrors] = useState(false);
  const errors = connectionErrors(draft, editing, t);
  const change = (patch: Partial<ProviderDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const shown = (name: keyof typeof errors) =>
    showErrors ? errors[name] : undefined;

  const pickTemplate = (kind: ProviderKind, template: ProviderTemplate) =>
    setDraft((current) => ({
      ...newDraft(kind, template, origin),
      slug: current.slug,
      displayName: current.displayName,
      redirectUrl: redirectUrlFor(current.slug, origin),
    }));
  const setSlug = (slug: string) =>
    setDraft((current) => ({
      ...current,
      slug,
      // The redirect URI follows the slug until it is edited by hand.
      redirectUrl:
        current.redirectUrl === redirectUrlFor(current.slug, origin)
          ? redirectUrlFor(slug, origin)
          : current.redirectUrl,
    }));

  const next = () => {
    if (step === STEP_CONNECTION && Object.keys(errors).length > 0) {
      setShowErrors(true);
      return;
    }
    setStep(step + 1);
  };

  const submit = async () => {
    setBusy(true);
    setError(null);
    let outcome: { message: string; warning?: string } | null = null;
    try {
      const name = draft.displayName.trim();
      if (provider) {
        await unwrap(
          replaceIdentityProvider({
            path: { providerId: provider.id },
            headers: { "If-Match": provider.version },
            body: toReplaceBody(draft, provider),
          }),
        );
        outcome = { message: t("providers.saved", { name }) };
      } else {
        const created = await unwrap(
          createIdentityProvider({ body: toCreateBody(draft) }),
        );
        let warning: string | undefined;
        if (draft.kind === "saml" && created) {
          try {
            await unwrap(
              importIdentityProviderMetadata({
                path: { providerId: created.id },
                body: draft.metadataXml.trim()
                  ? { xml: draft.metadataXml.trim() }
                  : { url: draft.metadataUrl.trim() },
              }),
            );
          } catch {
            warning = t("providers.metadataImportFailed");
          }
        }
        outcome = { message: t("providers.created", { name }), warning };
      }
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
    // The write is done: a failure to refresh afterwards is not a failure to save.
    if (outcome) await onDone(outcome.message, outcome.warning);
  };

  const title = provider
    ? t("providers.editTitle", { name: provider.displayName })
    : t("providers.wizardTitle");
  const last = step === STEP_POLICY;

  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      busy={busy}
      wide
      footer={
        <Space>
          <Button onClick={onClose} disabled={busy}>
            {t("providers.cancel")}
          </Button>
          {step > (editing ? STEP_CONNECTION : STEP_KIND) && (
            <Button disabled={busy} onClick={() => setStep(step - 1)}>
              {t("providers.back")}
            </Button>
          )}
          {last ? (
            <Button type="primary" loading={busy} onClick={() => void submit()}>
              {editing ? t("providers.save") : t("providers.create")}
            </Button>
          ) : (
            <Button type="primary" onClick={next}>
              {t("providers.next")}
            </Button>
          )}
        </Space>
      }
    >
      <div className="space-y-4">
        <Steps
          size="small"
          current={step}
          items={[
            { title: t("providers.stepKind") },
            { title: t("providers.stepConnection") },
            { title: t("providers.stepPolicy") },
          ]}
        />
        {error !== null && <ErrorBanner error={error} />}
        {step === STEP_KIND && (
          <>
            <Field label={t("providers.chooseProtocol")}>
              <Radio.Group
                value={draft.kind}
                onChange={(event) =>
                  pickTemplate(
                    event.target.value as ProviderKind,
                    draft.template,
                  )
                }
                options={[
                  { value: "oidc", label: t("providers.protocol.oidc") },
                  { value: "saml", label: t("providers.protocol.saml") },
                ]}
              />
            </Field>
            <Field label={t("providers.chooseTemplate")}>
              <Radio.Group
                value={draft.template}
                onChange={(event) =>
                  pickTemplate(
                    draft.kind,
                    event.target.value as ProviderTemplate,
                  )
                }
                options={PROVIDER_TEMPLATES.map((value) => ({
                  value,
                  label: t(`providers.template.${value}`),
                }))}
              />
              <p className="mt-2 text-sm text-fg-tertiary">
                {t(`providers.templateHint.${draft.template}`)}
              </p>
            </Field>
          </>
        )}
        {step === STEP_CONNECTION && (
          <>
            <Field
              label={t("providers.displayName")}
              hint={t("providers.displayNameHint")}
              error={shown("displayName")}
            >
              <Input
                value={draft.displayName}
                maxLength={120}
                onChange={(event) =>
                  change({ displayName: event.target.value })
                }
              />
            </Field>
            <Field
              label={t("providers.slug")}
              hint={t("providers.slugHint")}
              error={shown("slug")}
            >
              <Input
                value={draft.slug}
                maxLength={63}
                disabled={editing}
                className="font-mono"
                onChange={(event) => setSlug(event.target.value)}
              />
            </Field>
            {draft.kind === "oidc" ? (
              <>
                <Field
                  label={t("providers.issuer")}
                  hint={t(issuerHintKey(draft.template))}
                  error={shown("issuer")}
                >
                  <Input
                    value={draft.issuer}
                    placeholder="https://"
                    onChange={(event) => change({ issuer: event.target.value })}
                  />
                </Field>
                <Field
                  label={t("providers.clientId")}
                  error={shown("clientId")}
                >
                  <Input
                    value={draft.clientId}
                    onChange={(event) =>
                      change({ clientId: event.target.value })
                    }
                  />
                </Field>
                <Field
                  label={t("providers.clientSecret")}
                  hint={
                    editing && provider?.oidc?.clientSecretConfigured
                      ? t("providers.clientSecretKeep")
                      : undefined
                  }
                >
                  <Input.Password
                    value={draft.clientSecret}
                    autoComplete="new-password"
                    onChange={(event) =>
                      change({ clientSecret: event.target.value })
                    }
                  />
                </Field>
                {editing && provider?.oidc?.clientSecretConfigured && (
                  <Field label={t("providers.clearSecret")}>
                    <Switch
                      checked={draft.clearSecret}
                      onChange={(clearSecret) => change({ clearSecret })}
                    />
                  </Field>
                )}
                <Field
                  label={t("providers.redirectUrl")}
                  hint={t("providers.redirectUrlHint")}
                  error={shown("redirectUrl")}
                >
                  <Input
                    value={draft.redirectUrl}
                    className="font-mono"
                    onChange={(event) =>
                      change({ redirectUrl: event.target.value })
                    }
                  />
                </Field>
                <Field
                  label={t("providers.audience")}
                  hint={t("providers.audienceHint")}
                >
                  <Input
                    value={draft.audience}
                    onChange={(event) =>
                      change({ audience: event.target.value })
                    }
                  />
                </Field>
                <Field label={t("providers.scopes")}>
                  <Select<string[]>
                    mode="tags"
                    className="w-full"
                    aria-label={t("providers.scopes")}
                    value={draft.scopes}
                    onChange={(scopes) => change({ scopes })}
                    tokenSeparators={[" ", ","]}
                  />
                </Field>
                <Field
                  label={t("providers.adminSubjects")}
                  hint={t("providers.adminSubjectsHint")}
                >
                  <Select<string[]>
                    mode="tags"
                    className="w-full"
                    aria-label={t("providers.adminSubjects")}
                    value={draft.adminSubjects}
                    onChange={(adminSubjects) => change({ adminSubjects })}
                    tokenSeparators={[","]}
                  />
                </Field>
                <Field
                  label={t("providers.jwksUrl")}
                  hint={t("providers.jwksUrlHint")}
                >
                  <Input
                    value={draft.jwksUrl}
                    onChange={(event) =>
                      change({ jwksUrl: event.target.value })
                    }
                  />
                </Field>
              </>
            ) : (
              <>
                {!editing && (
                  <>
                    <Field
                      label={t("providers.metadataUrl")}
                      hint={t(metadataHintKey(draft.template))}
                      error={shown("metadata")}
                    >
                      <Input
                        value={draft.metadataUrl}
                        placeholder="https://"
                        onChange={(event) =>
                          change({ metadataUrl: event.target.value })
                        }
                      />
                    </Field>
                    <Field
                      label={t("providers.metadataXml")}
                      hint={t("providers.metadataXmlHint")}
                    >
                      <Input.TextArea
                        value={draft.metadataXml}
                        rows={4}
                        className="font-mono"
                        onChange={(event) =>
                          change({ metadataXml: event.target.value })
                        }
                      />
                    </Field>
                  </>
                )}
                <Field label={t("providers.samlSigning")}>
                  <Switch
                    checked={draft.signRequests}
                    onChange={(signRequests) => change({ signRequests })}
                  />
                </Field>
              </>
            )}
          </>
        )}
        {step === STEP_POLICY && (
          <>
            <Field label={t("providers.provisioningMode")}>
              <Select<ProvisioningMode>
                className="w-full"
                aria-label={t("providers.provisioningMode")}
                value={draft.provisioningMode}
                onChange={(provisioningMode) => change({ provisioningMode })}
                options={(["jit", "disabled", "scim_only"] as const).map(
                  (value) => ({ value, label: t(`providers.mode.${value}`) }),
                )}
              />
            </Field>
            <Field
              label={t("providers.jitDefaultRole")}
              hint={t("providers.jitDefaultRoleHint")}
            >
              <Select<"none" | "member">
                className="w-full"
                aria-label={t("providers.jitDefaultRole")}
                value={draft.jitDefaultRole}
                onChange={(jitDefaultRole) => change({ jitDefaultRole })}
                options={(["none", "member"] as const).map((value) => ({
                  value,
                  label: t(`providers.jitRole.${value}`),
                }))}
              />
            </Field>
            <Field
              label={t("providers.emailLinking")}
              hint={t("providers.emailLinkingHint")}
            >
              <Switch
                checked={draft.emailLinkingEnabled}
                onChange={(emailLinkingEnabled) =>
                  change({ emailLinkingEnabled })
                }
              />
            </Field>
            {(editing || draft.kind === "oidc") && (
              <Field
                label={t("providers.enableNow")}
                hint={t("providers.enableNowHint")}
              >
                <Switch
                  checked={draft.enabled}
                  onChange={(enabled) => change({ enabled })}
                />
              </Field>
            )}
            {draft.kind === "saml" && !editing && (
              <p className="text-sm text-fg-tertiary">
                {t("providers.samlNext")}
              </p>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
