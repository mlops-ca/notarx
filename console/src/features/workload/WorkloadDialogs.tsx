import { useState } from "react";
import { Button, Input, Select, Space, Switch } from "antd";
import {
  createWorkloadIssuer,
  createWorkloadTrustPolicy,
  type CreateWorkloadIssuer,
  type ServiceAccount,
  type WorkloadIssuer,
  type WorkloadIssuerKind,
  type WorkloadIssuerProbe,
} from "../../client";
import { ErrorBanner, Notice } from "../../components/ui/Feedback";
import { Field } from "../../components/ui/Layout";
import { Modal } from "../../components/ui/Modal";
import { usePreferences } from "../../lib/preferences";
import {
  parseConditions,
  recommendsSingleUse,
  templateJson,
} from "./policyTemplates";

const KINDS: WorkloadIssuerKind[] = [
  "github_actions",
  "gitlab_ci",
  "kubernetes",
  "generic",
];

/** The audiences textarea: one value per line, blanks dropped. */
export function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** Each write owns its form, busy state and error; the page hears only about success. */
export function IssuerDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<WorkloadIssuerKind>("github_actions");
  const [issuer, setIssuer] = useState("");
  const [audiences, setAudiences] = useState("");
  const [mode, setMode] = useState<"discovery" | "static">("discovery");
  const [jwksUri, setJwksUri] = useState("");
  const [staticJwks, setStaticJwks] = useState("");
  const [allowPrivate, setAllowPrivate] = useState(false);

  let staticValue: Record<string, unknown> | undefined;
  let staticInvalid = false;
  if (mode === "static") {
    try {
      const parsed: unknown = JSON.parse(staticJwks);
      if (
        typeof parsed === "object" &&
        parsed !== null &&
        !Array.isArray(parsed)
      ) {
        staticValue = parsed as Record<string, unknown>;
      } else {
        staticInvalid = true;
      }
    } catch {
      staticInvalid = staticJwks.trim() !== "";
    }
  }
  const complete =
    name.trim() !== "" &&
    issuer.trim() !== "" &&
    splitLines(audiences).length > 0 &&
    (mode === "discovery" || staticValue !== undefined);

  const submit = async () => {
    setBusy(true);
    setError(null);
    const body: CreateWorkloadIssuer = {
      name: name.trim(),
      kind,
      issuer: issuer.trim(),
      audiences: splitLines(audiences),
      jwksMode: mode,
      allowPrivateNetwork: allowPrivate,
    };
    if (mode === "discovery" && jwksUri.trim()) body.jwksUri = jwksUri.trim();
    if (mode === "static") body.staticJwks = staticValue;
    try {
      const { error: createError } = await createWorkloadIssuer({ body });
      if (createError) {
        setError(createError);
        return;
      }
      setName("");
      setIssuer("");
      setAudiences("");
      setJwksUri("");
      setStaticJwks("");
      await onCreated();
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title={t("workload.newIssuer")}
      onClose={onClose}
      busy={busy}
      footer={
        <Space>
          <Button onClick={onClose} disabled={busy}>
            {t("workload.cancel")}
          </Button>
          <Button
            type="primary"
            loading={busy}
            disabled={!complete}
            onClick={() => void submit()}
          >
            {t("workload.create")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        {error !== null && <ErrorBanner error={error} />}
        <Field label={t("workload.name")} hint={t("workload.issuerNameHint")}>
          <Input
            value={name}
            maxLength={63}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("workload.kind")}>
          <Select<WorkloadIssuerKind>
            className="w-full"
            value={kind}
            onChange={setKind}
            options={KINDS.map((value) => ({
              value,
              label: t(`workload.kind.${value}`),
            }))}
          />
        </Field>
        <Field label={t("workload.issuerUrl")}>
          <Input
            value={issuer}
            placeholder="https://"
            onChange={(event) => setIssuer(event.target.value)}
          />
        </Field>
        <Field
          label={t("workload.audiences")}
          hint={t("workload.audiencesHint")}
        >
          <Input.TextArea
            value={audiences}
            rows={2}
            onChange={(event) => setAudiences(event.target.value)}
          />
        </Field>
        <Field label={t("workload.keySource")}>
          <Select<"discovery" | "static">
            className="w-full"
            value={mode}
            onChange={setMode}
            options={[
              { value: "discovery", label: t("workload.keySourceDiscovery") },
              { value: "static", label: t("workload.keySourceStatic") },
            ]}
          />
        </Field>
        {mode === "discovery" ? (
          <Field label={t("workload.jwksUri")} hint={t("workload.jwksUriHint")}>
            <Input
              value={jwksUri}
              onChange={(event) => setJwksUri(event.target.value)}
            />
          </Field>
        ) : (
          <Field
            label={t("workload.staticJwks")}
            hint={t("workload.staticJwksHint")}
            error={staticInvalid ? t("workload.invalidJson") : undefined}
          >
            <Input.TextArea
              value={staticJwks}
              rows={5}
              className="font-mono"
              onChange={(event) => setStaticJwks(event.target.value)}
            />
          </Field>
        )}
        <Field
          label={t("workload.allowPrivateNetwork")}
          hint={t("workload.allowPrivateNetworkHint")}
        >
          <Switch checked={allowPrivate} onChange={setAllowPrivate} />
        </Field>
      </div>
    </Modal>
  );
}

export function PolicyDialog({
  open,
  issuers,
  accounts,
  onClose,
  onCreated,
}: {
  open: boolean;
  issuers: WorkloadIssuer[];
  accounts: ServiceAccount[];
  onClose: () => void;
  onCreated: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [issuerId, setIssuerId] = useState("");
  const [accountId, setAccountId] = useState("");
  const [conditions, setConditions] = useState("");
  const [ttl, setTtl] = useState("900");
  const [exchangeOnly, setExchangeOnly] = useState(false);
  const [singleUse, setSingleUse] = useState(false);
  const [sampleToken, setSampleToken] = useState("");
  const [allowAnyRef, setAllowAnyRef] = useState(false);
  const [allowAdmin, setAllowAdmin] = useState(false);

  const issuer = issuers.find((candidate) => candidate.id === issuerId);
  const parsed = parseConditions(conditions);
  const ttlSeconds = Number.parseInt(ttl, 10);
  const complete =
    name.trim() !== "" &&
    issuer !== undefined &&
    accountId !== "" &&
    parsed !== null &&
    Number.isInteger(ttlSeconds) &&
    ttlSeconds >= 1 &&
    ttlSeconds <= 86400;

  const chooseIssuer = (id: string) => {
    setIssuerId(id);
    const chosen = issuers.find((candidate) => candidate.id === id);
    // The template of the kind fills the mandatory conditions; the operator edits the placeholders.
    if (chosen) setConditions(templateJson(chosen.kind));
  };
  const chooseExchangeOnly = (value: boolean) => {
    setExchangeOnly(value);
    setSingleUse(recommendsSingleUse(value));
  };

  const submit = async () => {
    if (!issuer || !parsed) return;
    setBusy(true);
    setError(null);
    try {
      const { error: createError } = await createWorkloadTrustPolicy({
        body: {
          name: name.trim(),
          issuerId: issuer.id,
          serviceAccountId: accountId,
          conditions: parsed,
          maxTokenTtlSeconds: ttlSeconds,
          singleUse,
          allowAnyRef,
          allowAdmin,
          sampleToken: sampleToken.trim() || undefined,
        },
      });
      if (createError) {
        setError(createError);
        return;
      }
      setName("");
      setConditions("");
      setIssuerId("");
      setSampleToken("");
      await onCreated();
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title={t("workload.newPolicy")}
      onClose={onClose}
      busy={busy}
      footer={
        <Space>
          <Button onClick={onClose} disabled={busy}>
            {t("workload.cancel")}
          </Button>
          <Button
            type="primary"
            loading={busy}
            disabled={!complete}
            onClick={() => void submit()}
          >
            {t("workload.create")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        {error !== null && <ErrorBanner error={error} />}
        <Field
          label={t("workload.policyName")}
          hint={t("workload.policyNameHint")}
        >
          <Input
            value={name}
            maxLength={63}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("workload.issuer")}>
          <Select<string>
            className="w-full"
            value={issuerId || undefined}
            placeholder={t("workload.issuerRequired")}
            onChange={chooseIssuer}
            options={issuers.map((candidate) => ({
              value: candidate.id,
              label: candidate.name,
            }))}
          />
        </Field>
        <Field label={t("workload.serviceAccount")}>
          <Select<string>
            className="w-full"
            value={accountId || undefined}
            placeholder={t("workload.serviceAccountRequired")}
            onChange={setAccountId}
            options={accounts.map((account) => ({
              value: account.id,
              label: account.name,
            }))}
          />
        </Field>
        {issuer && (
          <Notice
            tone="info"
            closable={false}
            title={t("workload.template")}
            description={`${t(`workload.template.${issuer.kind}`)}. ${t("workload.templateHint")}`}
          />
        )}
        <Field
          label={t("workload.conditionsJson")}
          hint={t("workload.conditionsHint")}
          error={
            conditions.trim() !== "" && parsed === null
              ? t("workload.conditionsInvalid")
              : undefined
          }
        >
          <Input.TextArea
            value={conditions}
            rows={8}
            className="font-mono"
            onChange={(event) => setConditions(event.target.value)}
          />
        </Field>
        <Field label={t("workload.maxTtl")} hint={t("workload.maxTtlHint")}>
          <Input
            value={ttl}
            inputMode="numeric"
            onChange={(event) => setTtl(event.target.value)}
          />
        </Field>
        <Field
          label={t("workload.exchangeOnly")}
          hint={t("workload.exchangeOnlyHint")}
        >
          <Switch checked={exchangeOnly} onChange={chooseExchangeOnly} />
        </Field>
        <Field
          label={t("workload.singleUseLabel")}
          hint={t("workload.singleUseHint")}
        >
          <Switch checked={singleUse} onChange={setSingleUse} />
        </Field>
        {singleUse && issuer?.kind === "generic" && (
          <Field
            label={t("workload.sampleToken")}
            hint={t("workload.sampleTokenHint")}
          >
            <Input.TextArea
              value={sampleToken}
              rows={3}
              className="font-mono"
              onChange={(event) => setSampleToken(event.target.value)}
            />
          </Field>
        )}
        <Field
          label={t("workload.allowAnyRef")}
          hint={t("workload.allowAnyRefHint")}
        >
          <Switch checked={allowAnyRef} onChange={setAllowAnyRef} />
        </Field>
        <Field
          label={t("workload.allowAdmin")}
          hint={t("workload.allowAdminHint")}
        >
          <Switch checked={allowAdmin} onChange={setAllowAdmin} />
        </Field>
      </div>
    </Modal>
  );
}

export function ProbeDialog({
  probe,
  issuerName,
  onClose,
}: {
  probe: WorkloadIssuerProbe | null;
  issuerName: string;
  onClose: () => void;
}) {
  const { t } = usePreferences();
  return (
    <Modal
      open={probe !== null}
      title={`${t("workload.probeTitle")}: ${issuerName}`}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t("workload.close")}</Button>}
    >
      {probe && (
        <div className="space-y-3 text-sm">
          <Notice
            tone={probe.reachable ? "success" : "warning"}
            closable={false}
            title={
              probe.reachable
                ? t("workload.probeReachable")
                : t("workload.probeUnreachable")
            }
            description={probe.error}
          />
          {probe.reachable && (
            <>
              <div>
                {t("workload.probeAlgorithms")}: {probe.algorithms.join(", ")}
              </div>
              <div>
                {t("workload.probeKeys")}:{" "}
                {probe.keys
                  .map((key) => `${key.keyId ?? "-"} (${key.keyType})`)
                  .join(", ")}
              </div>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
