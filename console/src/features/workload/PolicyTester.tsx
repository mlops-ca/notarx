import { useState } from "react";
import { PlayCircleOutlined } from "@ant-design/icons";
import { Button, Input, Select } from "antd";
import {
  testWorkloadTrustPolicy,
  type WorkloadIssuer,
  type WorkloadPolicyTest,
} from "../../client";
import { ErrorBanner, Notice } from "../../components/ui/Feedback";
import { Card, CardHeader, Field } from "../../components/ui/Layout";
import { usePreferences } from "../../lib/preferences";

/**
 * The dry run of ADR 0019: a pasted token or claim set is evaluated against the
 * policies of an issuer and the failing condition is shown. Nothing is saved.
 */
export function PolicyTester({ issuers }: { issuers: WorkloadIssuer[] }) {
  const { t } = usePreferences();
  const [input, setInput] = useState("");
  const [issuerId, setIssuerId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<WorkloadPolicyTest | null>(null);

  const run = async () => {
    const text = input.trim();
    if (!text) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const body: Parameters<typeof testWorkloadTrustPolicy>[0]["body"] = {};
      if (text.startsWith("{")) {
        body.claims = JSON.parse(text) as Record<string, unknown>;
      } else {
        body.token = text;
      }
      if (issuerId) body.issuerId = issuerId;
      const { data, error: testError } = await testWorkloadTrustPolicy({
        body,
      });
      if (testError || !data) {
        setError(testError ?? new Error(t("workload.invalidJson")));
        return;
      }
      setResult(data);
    } catch {
      setError(new Error(t("workload.invalidJson")));
    } finally {
      setBusy(false);
    }
  };

  const matched = result?.results.find(
    (item) => item.policyId === result.matchedPolicyId,
  );

  return (
    <Card>
      <CardHeader title={t("workload.tester")} />
      <div className="space-y-4 p-5">
        <p className="text-sm text-fg-tertiary">{t("workload.testerHint")}</p>
        {error !== null && <ErrorBanner error={error} />}
        <Field label={t("workload.testerIssuer")}>
          <Select<string>
            className="w-full"
            value={issuerId}
            onChange={setIssuerId}
            options={[
              { value: "", label: t("workload.testerAnyIssuer") },
              ...issuers.map((issuer) => ({
                value: issuer.id,
                label: issuer.name,
              })),
            ]}
          />
        </Field>
        <Field
          label={t("workload.testerInput")}
          hint={t("workload.testerInputHint")}
        >
          <Input.TextArea
            value={input}
            rows={5}
            className="font-mono"
            onChange={(event) => setInput(event.target.value)}
          />
        </Field>
        <Button
          type="primary"
          icon={<PlayCircleOutlined />}
          loading={busy}
          disabled={!input.trim()}
          onClick={() => void run()}
        >
          {t("workload.run")}
        </Button>
        {result && (
          <div className="space-y-3 text-sm" data-testid="workload-test-result">
            <Notice
              tone={matched ? "success" : "warning"}
              closable={false}
              title={
                matched
                  ? t("workload.testerMatched", { name: matched.policyName })
                  : t("workload.testerNoMatch")
              }
              description={
                result.signatureVerified
                  ? t("workload.testerVerified")
                  : (result.verificationError ?? t("workload.testerUnverified"))
              }
            />
            {result.ambiguous && (
              <Notice
                tone="warning"
                closable={false}
                title={t("workload.testerAmbiguous")}
              />
            )}
            <ul className="space-y-1">
              {result.results.map((item) => (
                <li key={item.policyId}>
                  {item.failure
                    ? t("workload.testerFailure", {
                        name: item.policyName,
                        index: String(item.failure.conditionIndex),
                        claim: item.failure.claim.join("."),
                        reason: item.failure.reason,
                      })
                    : t("workload.testerPass", { name: item.policyName })}
                </li>
              ))}
            </ul>
            {result.warnings.length > 0 && (
              <Notice
                tone="warning"
                closable={false}
                title={t("workload.testerWarnings")}
                description={result.warnings.join(" ")}
              />
            )}
          </div>
        )}
      </div>
    </Card>
  );
}
