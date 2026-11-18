import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { DiagnosticLocalCapacity } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { LocalCapacityPanel } from "./LocalCapacityPanel";

afterEach(cleanup);

const snapshot: DiagnosticLocalCapacity = {
  checkedAt: "2026-10-03T09:00:00Z",
  source: "statfs",
  scope: "observer_mount_namespace",
  unit: "bytes",
  refreshIntervalSeconds: 15,
  maxSampleAgeSeconds: 60,
  timeoutMilliseconds: 1000,
  mounts: [
    {
      alias: "temporary",
      status: "available",
      sharedWith: ["logs"],
      totalBytes: 40960,
      availableBytes: 0,
      sampleAt: "2026-10-03T08:59:59Z",
    },
    {
      alias: "logs",
      status: "unknown",
      reason: "remote_filesystem",
      sharedWith: [],
    },
    {
      alias: "backups",
      status: "stale",
      reason: "timeout",
      sharedWith: [],
      sampleAt: "2026-10-03T08:58:00Z",
      totalBytes: 999999,
      availableBytes: 999999,
    },
  ],
};

describe("LocalCapacityPanel", () => {
  it("shows mount perspective, valid zero, fixed reason and old sample without stale bytes", () => {
    render(
      <PreferencesProvider>
        <LocalCapacityPanel capacity={snapshot} />
      </PreferencesProvider>,
    );
    expect(
      screen.getByRole("heading", { name: "Local mount capacity" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /This process's mount view is not the S3 or NAS backend physical pool or a Repository logical quota\. Do not add shared aliases' capacity\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /This process's mount view is not the S3 or NAS backend physical pool or a Repository logical quota\. Do not add shared aliases' capacity\./,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("0 B")).toBeInTheDocument();
    expect(
      screen.getByText("Remote filesystem; capacity unknown"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Refresh timed out; capacity unknown"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Shares the visible filesystem with Logs/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/999999|976/)).not.toBeInTheDocument();
  });

  it("reports an older node without localCapacity as unknown rather than zero", () => {
    render(
      <PreferencesProvider>
        <LocalCapacityPanel />
      </PreferencesProvider>,
    );
    expect(
      screen.getByText(
        "This node does not report local capacity; capacity unknown",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("0 B")).not.toBeInTheDocument();
  });
});
