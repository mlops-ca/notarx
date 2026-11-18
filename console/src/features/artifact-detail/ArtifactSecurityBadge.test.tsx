import { createTranslator } from "../../messages/translate";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ArtifactIntelligenceSummary } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { ArtifactSecurityBadge } from "./ArtifactSecurityBadge";

const t = createTranslator("en-CA");
const renderBadge = (summary?: ArtifactIntelligenceSummary) =>
  render(
    <PreferencesProvider>
      <ArtifactSecurityBadge summary={summary} t={t} />
    </PreferencesProvider>,
  );

afterEach(cleanup);

describe("ArtifactSecurityBadge", () => {
  it("renders the stable status labels and finding count", () => {
    renderBadge({
      signatureCount: 1,
      sbomCount: 1,
      licenseCount: 1,
      vulnerabilityStatus: "affected",
      critical: 2,
      high: 1,
    });
    expect(screen.getByText("Affected · 3")).toBeInTheDocument();
  });

  it.each([
    [undefined, "Not scanned"],
    [{ signatureCount: 0, sbomCount: 1, licenseCount: 0 }, "1 evidence"],
    [
      {
        signatureCount: 0,
        sbomCount: 0,
        licenseCount: 0,
        vulnerabilityStatus: "clean",
      },
      "Clean",
    ],
    [
      {
        signatureCount: 0,
        sbomCount: 0,
        licenseCount: 0,
        vulnerabilityStatus: "error",
      },
      "Scan error",
    ],
  ])("renders %s as %s", (summary, label) => {
    renderBadge(summary as ArtifactIntelligenceSummary | undefined);
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});
