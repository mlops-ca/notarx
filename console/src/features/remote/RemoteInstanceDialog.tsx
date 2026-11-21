import { useState } from "react";
import { Checkbox, Input, Radio } from "antd";
import {
  createRemoteInstance,
  updateRemoteInstance,
  type RemoteInstance,
  type RemoteInstanceAuthKind,
} from "../../client";
import { Field } from "../../components/ui/Layout";
import { FormModal } from "../../components/ui/Modal";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";

const AUTH_KINDS: readonly RemoteInstanceAuthKind[] = [
  "workload_identity",
  "static_token",
];

/**
 * Adds a remote instance, or edits one when `instance` is set. The token is
 * write-only: the field starts empty, is never filled from the server, and an
 * empty value on edit keeps the sealed token.
 */
export function RemoteInstanceDialog({
  open,
  instance,
  onClose,
  onSaved,
}: {
  open: boolean;
  instance: RemoteInstance | null;
  onClose: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const [wasOpen, setWasOpen] = useState(false);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [authKind, setAuthKind] =
    useState<RemoteInstanceAuthKind>("workload_identity");
  const [audience, setAudience] = useState("");
  const [token, setToken] = useState("");
  const [trustEvidence, setTrustEvidence] = useState(false);
  const [enabled, setEnabled] = useState(true);
  // Each opening starts from the instance being edited, or from blanks.
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setName(instance?.name ?? "");
      setBaseUrl(instance?.baseUrl ?? "");
      setAuthKind(instance?.authKind ?? "workload_identity");
      setAudience(instance?.audience ?? "");
      setToken("");
      setTrustEvidence(instance?.trustEvidence ?? false);
      setEnabled(instance ? instance.state !== "disabled" : true);
    }
  }

  const isToken = authKind === "static_token";
  const tokenKept = isToken && instance?.authKind === "static_token";
  const complete =
    name.trim() !== "" &&
    baseUrl.trim() !== "" &&
    (isToken
      ? token !== "" || (tokenKept && instance?.tokenConfigured === true)
      : audience.trim() !== "");

  const submit = async () => {
    const common = {
      name: name.trim(),
      baseUrl: baseUrl.trim(),
      authKind,
      trustEvidence,
      ...(isToken ? {} : { audience: audience.trim() }),
      ...(isToken && token !== "" ? { token } : {}),
    };
    if (instance) {
      await unwrap(
        updateRemoteInstance({
          path: { remoteInstanceId: instance.id },
          headers: { "If-Match": instance.version },
          body: { ...common, enabled },
        }),
      );
    } else {
      await unwrap(createRemoteInstance({ body: common }));
    }
    setToken("");
    await onSaved();
  };

  return (
    <FormModal
      open={open}
      title={instance ? t("remoteInstances.edit") : t("remoteInstances.add")}
      submitLabel={
        instance ? t("remoteInstances.save") : t("remoteInstances.create")
      }
      canSubmit={complete}
      onSubmit={submit}
      onClose={onClose}
    >
      <Field label={t("remoteInstances.name")}>
        <Input
          value={name}
          maxLength={120}
          autoComplete="off"
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field
        label={t("remoteInstances.baseUrl")}
        hint={t("remoteInstances.baseUrlHint")}
      >
        <Input
          value={baseUrl}
          placeholder="https://notarx.example.test"
          autoComplete="off"
          onChange={(event) => setBaseUrl(event.target.value)}
        />
      </Field>
      <Field label={t("remoteInstances.auth")} group>
        <Radio.Group
          value={authKind}
          onChange={(event) => setAuthKind(event.target.value)}
          options={AUTH_KINDS.map((kind) => ({
            value: kind,
            label: t(`remoteInstances.auth.${kind}`),
          }))}
        />
      </Field>
      {isToken ? (
        <Field
          label={t("remoteInstances.token")}
          hint={
            tokenKept && instance?.tokenConfigured
              ? t("remoteInstances.tokenReplaceHint")
              : t("remoteInstances.tokenHint")
          }
        >
          <Input.Password
            value={token}
            autoComplete="new-password"
            visibilityToggle={false}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
      ) : (
        <Field
          label={t("remoteInstances.audience")}
          hint={t("remoteInstances.audienceHint")}
        >
          <Input
            value={audience}
            autoComplete="off"
            onChange={(event) => setAudience(event.target.value)}
          />
        </Field>
      )}
      <Checkbox
        checked={trustEvidence}
        onChange={(event) => setTrustEvidence(event.target.checked)}
      >
        {t("remoteInstances.trustEvidence")}
      </Checkbox>
      {instance && (
        <Checkbox
          checked={enabled}
          onChange={(event) => setEnabled(event.target.checked)}
        >
          {t("remoteInstances.enabled")}
        </Checkbox>
      )}
    </FormModal>
  );
}
