import { createTranslator } from "../../messages/translate";
import { describe, expect, it } from "vitest";
import { auditOperationLabel } from "./auditOperations";

const en = createTranslator("en-CA");

describe("auditOperationLabel", () => {
  it("translates the traffic operations derived from HTTP methods", () => {
    expect(auditOperationLabel("get", en)).toBe("Read (GET)");
    expect(auditOperationLabel("put", en)).toBe("Upload (PUT)");
    expect(auditOperationLabel("delete", en)).toBe("Delete (DELETE)");
  });

  it("translates every management code family", () => {
    const samples: [string, string, string][] = [
      [
        "repository.grants.upsert",
        "Upsert repository grant",
        "Upsert repository grant",
      ],
      [
        "repository.grants.delete",
        "Delete repository grant",
        "Delete repository grant",
      ],
      ["artifact.tombstone", "Tombstone artifact", "Tombstone artifact"],
      ["user.login", "User sign-in", "User sign-in"],
      ["promote", "Promote artifact", "Promote artifact"],
      [
        "apt.repository_snapshot.publish",
        "Publish APT snapshot",
        "Publish APT snapshot",
      ],
      [
        "webhook.subscription.create",
        "Create webhook subscription",
        "Create webhook subscription",
      ],
      [
        "lifecycle.intelligence_reconcile",
        "Reconcile artifact intelligence",
        "Reconcile artifact intelligence",
      ],
      ["capacity.configure", "Configure capacity", "Configure capacity"],
      [
        "service_account.credential.revoke",
        "Revoke service account credential",
        "Revoke service account credential",
      ],
    ];
    for (const [code, chinese, english] of samples) {
      expect(auditOperationLabel(code, en)).toBe(chinese);
      expect(auditOperationLabel(code, en)).toBe(english);
    }
  });

  it("labels the codes denial audits and APT package operations write", () => {
    const samples: [string, string, string][] = [
      ["admin", "Repository admin", "Repository admin"],
      ["intelligence", "Artifact intelligence", "Artifact intelligence"],
      ["apt.package.promote", "Promote APT package", "Promote APT package"],
      [
        "apt.package.replicate",
        "Replicate APT package",
        "Replicate APT package",
      ],
      ["apt.package.delete", "Delete APT package", "Delete APT package"],
      ["apt.package.restore", "Restore APT package", "Restore APT package"],
      ["apt.package.retention", "Retain APT package", "Retain APT package"],
    ];
    for (const [code, chinese, english] of samples) {
      expect(auditOperationLabel(code, en)).toBe(chinese);
      expect(auditOperationLabel(code, en)).toBe(english);
    }
  });

  it("keeps an unknown code readable by showing the code itself", () => {
    expect(auditOperationLabel("future.feature.action", en)).toBe(
      "future.feature.action",
    );
    expect(auditOperationLabel("future.feature.action", en)).toBe(
      "future.feature.action",
    );
  });
});
