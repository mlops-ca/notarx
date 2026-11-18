import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getDiagnostics } from "../../client";
import type { Diagnostics } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { SystemDiagnosticsPanel } from "./SystemDiagnosticsPanel";
import { TestQueryProvider } from "../../test/queryClient";
import { createQueryClient } from "../../lib/query/client";

vi.mock("../../client", () => ({ getDiagnostics: vi.fn() }));
const mockGetDiagnostics = vi.mocked(getDiagnostics);

const diagnostics: Diagnostics = {
  generatedAt: "2026-08-10T08:00:00Z",
  build: {
    version: "v1.2.3",
    revision: "abc123",
    goVersion: "go1.test",
    modified: false,
  },
  runtime: {
    instanceId: "gateway-01",
    roles: ["standalone"],
    workerFormats: ["maven"],
    workerKinds: ["retention"],
  },
  dependencies: [
    { name: "postgresql", status: "reachable", detail: "reachable" },
    {
      name: "object-storage",
      status: "unreachable",
      detail: "health check failed",
    },
  ],
  scanner: {
    name: "trivy",
    formats: ["maven", "oci"],
    status: "healthy",
    detail: "scanner health check passed",
    checkedAt: "2026-08-10T08:00:00Z",
    version: "0.61.0",
    databaseFreshness: "fresh",
    databaseVersion: "2026-08-10",
    databaseUpdatedAt: "2026-08-10T06:00:00Z",
    databaseMaxAgeSeconds: 86400,
  },
  queues: [
    {
      kind: "promotion",
      format: "maven",
      state: "failed",
      count: 2,
      oldestCreatedAt: "2026-08-10T07:00:00Z",
    },
  ],
  nodes: { status: "degraded", online: 1, stale: 1, offline: 0, issues: [] },
};

const renderPanel = () =>
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <SystemDiagnosticsPanel />
      </PreferencesProvider>
    </TestQueryProvider>,
  );

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe("SystemDiagnosticsPanel", () => {
  it("shows dev for a Console without build injection and does not report a mismatch", async () => {
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    renderPanel();

    const label = await screen.findByText("Console version");
    expect(
      within(label.parentElement as HTMLElement).getByText("dev"),
    ).toBeInTheDocument();
    expect(screen.getByText("Gateway version")).toBeInTheDocument();
    expect(
      screen.queryByText("Console and Gateway versions differ"),
    ).not.toBeInTheDocument();
  });

  it("shows the injected Console build and matching Gateway version without a warning", async () => {
    vi.stubGlobal("__CONSOLE_BUILD_VERSION__", "1.2.3");
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    renderPanel();

    const label = await screen.findByText("Console version");
    expect(
      within(label.parentElement as HTMLElement).getByText("1.2.3"),
    ).toBeInTheDocument();
    expect(screen.getByText("v1.2.3")).toBeInTheDocument();
    expect(
      screen.queryByText("Console and Gateway versions differ"),
    ).not.toBeInTheDocument();
  });

  it("explains a known build mismatch next to the identity fields", async () => {
    vi.stubGlobal("__CONSOLE_BUILD_VERSION__", "1.2.2");
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    renderPanel();

    const identityHeading = await screen.findByRole("heading", {
      name: "Build and runtime identity",
    });
    const card = identityHeading.closest(".ag-card") as HTMLElement;
    expect(within(card).getByText("1.2.2")).toBeInTheDocument();
    expect(
      within(card).getByText("Console and Gateway versions differ"),
    ).toBeInTheDocument();
    expect(
      within(card).getByText(
        /Image tags may differ, or the browser may have cached an older bundle\. Check the deployed versions and refresh the page\./,
      ),
    ).toBeInTheDocument();
  });

  it.each(["dev", "unknown", ""])(
    "does not infer a mismatch from Gateway build %j",
    async (version) => {
      vi.stubGlobal("__CONSOLE_BUILD_VERSION__", "1.2.3");
      mockGetDiagnostics.mockResolvedValue({
        data: { ...diagnostics, build: { ...diagnostics.build, version } },
      } as never);
      renderPanel();
      await screen.findByText("Console version");
      expect(
        screen.queryByText("Console and Gateway versions differ"),
      ).not.toBeInTheDocument();
    },
  );

  it("renders dependency, build, node, and queue diagnostics", async () => {
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    renderPanel();

    expect(
      await screen.findByText("System health needs attention"),
    ).toBeInTheDocument();
    expect(screen.getByText("v1.2.3")).toBeInTheDocument();
    expect(screen.getByText("gateway-01")).toBeInTheDocument();
    expect(screen.getByText("Worker formats")).toBeInTheDocument();
    expect(screen.getByText("retention")).toBeInTheDocument();
    expect(screen.getByText("Clean")).toBeInTheDocument();
    expect(screen.getByText("PostgreSQL")).toBeInTheDocument();
    expect(screen.getByText("Object storage")).toBeInTheDocument();
    expect(screen.getByText("Unreachable")).toBeInTheDocument();
    expect(screen.getByText(/Artifact scanner · trivy/)).toBeInTheDocument();
    expect(
      screen.getByText(/Vulnerability database fresh/),
    ).toBeInTheDocument();
    expect(screen.getByText("Healthy")).toBeInTheDocument();
    expect(screen.getByText("promotion")).toBeInTheDocument();
    expect(screen.getByText("maven")).toBeInTheDocument();
  });

  it("separates build metadata from runtime identity", async () => {
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    const { container } = renderPanel();

    const identityHeading = await screen.findByRole("heading", {
      name: "Build and runtime identity",
    });
    const identityCard = identityHeading.closest(".ag-card");
    expect(identityCard).not.toBeNull();
    expect(
      within(identityCard as HTMLElement).getByRole("heading", {
        name: "Build information",
      }),
    ).toBeInTheDocument();
    expect(
      within(identityCard as HTMLElement).getByRole("heading", {
        name: "Runtime identity",
      }),
    ).toBeInTheDocument();
    expect(
      container.querySelector(
        ".ag-diagnostics-identity-card .ant-descriptions",
      ),
    ).toBeNull();
  });

  it("surfaces node issues in the diagnostic action list", async () => {
    mockGetDiagnostics.mockResolvedValue({
      data: {
        ...diagnostics,
        nodes: {
          ...diagnostics.nodes,
          issues: [
            {
              code: "worker-capacity-missing",
              message: "No node can run OCI scan jobs",
            },
          ],
        },
      },
    } as never);
    renderPanel();

    expect(
      await screen.findByRole("heading", { name: "Diagnostic snapshot" }),
    ).toBeInTheDocument();
    expect(screen.getByText("worker-capacity-missing")).toBeInTheDocument();
    expect(
      screen.getByText("No node can run OCI scan jobs"),
    ).toBeInTheDocument();
  });

  it("copies only the sanitized server response", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    mockGetDiagnostics.mockResolvedValue({ data: diagnostics } as never);
    renderPanel();

    await user.click(
      await screen.findByRole("button", {
        name: "Copy sanitized diagnostics JSON",
      }),
    );
    expect(writeText).toHaveBeenCalledOnce();
    const copied = writeText.mock.calls[0]?.[0] as string;
    expect(copied).toContain('"instanceId": "gateway-01"');
    expect(copied).not.toMatch(/password|token|databaseUrl/i);
    expect(screen.getByText("Copied")).toBeInTheDocument();
  });

  it("reports no dependency checks as neutral, not healthy", async () => {
    mockGetDiagnostics.mockResolvedValue({
      data: { ...diagnostics, dependencies: [] },
    } as never);
    renderPanel();

    const summary = within(
      await screen.findByRole("group", { name: "Page summary" }),
    );
    const dependencyMetric = summary.getByText("Dependencies ready")
      .parentElement as HTMLElement;
    expect(
      within(dependencyMetric).getByText("No dependency checks reported"),
    ).toBeVisible();
    expect(within(dependencyMetric).getByText("0").className).not.toContain(
      "status-success",
    );
  });

  it("treats unconfigured dependencies as unavailable", async () => {
    mockGetDiagnostics.mockResolvedValue({
      data: {
        ...diagnostics,
        dependencies: [
          {
            name: "runtime",
            status: "not_configured",
            detail: "Not configured",
          },
        ],
      },
    } as never);
    renderPanel();

    expect(
      await screen.findByText("System health needs attention"),
    ).toBeInTheDocument();
    const summary = within(screen.getByRole("group", { name: "Page summary" }));
    const dependencyMetric =
      summary.getByText("Dependencies ready").parentElement;
    expect(dependencyMetric).not.toBeNull();
    expect(
      within(dependencyMetric as HTMLElement).getByText("0"),
    ).toBeInTheDocument();
    expect(
      within(dependencyMetric as HTMLElement).getByText("1 checks"),
    ).toBeInTheDocument();
  });

  it("surfaces a stale vulnerability database as a health warning", async () => {
    mockGetDiagnostics.mockResolvedValue({
      data: {
        ...diagnostics,
        dependencies: [
          { name: "postgresql", status: "reachable", detail: "reachable" },
        ],
        queues: [],
        nodes: {
          status: "healthy",
          online: 1,
          stale: 0,
          offline: 0,
          issues: [],
        },
        scanner: {
          ...diagnostics.scanner,
          status: "degraded",
          detail: "vulnerability database is stale",
          databaseFreshness: "stale",
          databaseUpdatedAt: "2026-08-08T06:00:00Z",
        },
      },
    } as never);
    renderPanel();

    expect(
      await screen.findByText("System health needs attention"),
    ).toBeInTheDocument();
    expect(
      screen.getAllByText(/Vulnerability database stale · .+/).length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByText("Degraded").length).toBeGreaterThan(0);
  });

  it("remains compatible with diagnostics from an older rolling-upgrade node", async () => {
    const legacyDiagnostics = { ...diagnostics };
    delete legacyDiagnostics.scanner;
    mockGetDiagnostics.mockResolvedValue({ data: legacyDiagnostics } as never);
    renderPanel();

    expect(await screen.findByText("PostgreSQL")).toBeInTheDocument();
    expect(screen.queryByText(/Artifact scanner/)).not.toBeInTheDocument();
  });

  it("retains the previous snapshot on a refresh failure and clears it when authorization is lost", async () => {
    const user = userEvent.setup();
    mockGetDiagnostics
      .mockResolvedValueOnce({ data: diagnostics } as never)
      .mockRejectedValueOnce({
        status: 503,
        code: "internal_error",
        message: "Synthetic refresh unavailable",
      })
      .mockRejectedValueOnce({
        status: 403,
        code: "forbidden",
        message: "Synthetic permission denied",
      });
    renderPanel();
    await user.click(
      await screen.findByRole("button", { name: "Refresh diagnostics" }),
    );
    expect(
      await screen.findByText(
        "Refresh failed; showing the previous diagnostic snapshot",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("gateway-01")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Refresh diagnostics" }),
    );
    expect(
      await screen.findByText("Synthetic permission denied"),
    ).toBeInTheDocument();
    expect(screen.queryByText("gateway-01")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Local mount capacity" }),
    ).not.toBeInTheDocument();
  });

  it.each(["Synthetic non-JSON denial", ""])(
    "clears old capacity on HTTP 403 even with body %j",
    async (body) => {
      const user = userEvent.setup();
      mockGetDiagnostics
        .mockResolvedValueOnce({ data: diagnostics } as never)
        .mockResolvedValueOnce({
          error: body,
          response: new Response(body, { status: 403 }),
        } as never);
      renderPanel();
      await user.click(
        await screen.findByRole("button", { name: "Refresh diagnostics" }),
      );
      await screen.findByText("Request failed");
      expect(screen.queryByText("gateway-01")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("heading", { name: "Local mount capacity" }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByText("Loading diagnostics…"),
      ).not.toBeInTheDocument();
    },
  );

  it.each([401, 403])(
    "handles HTTP %i with the SDK's empty-object error and hides cached diagnostics",
    async (status) => {
      const user = userEvent.setup();
      const onAuthFailure = vi.fn();
      const client = createQueryClient(onAuthFailure);
      client.setDefaultOptions({ queries: { retry: false } });
      mockGetDiagnostics
        .mockResolvedValueOnce({ data: diagnostics } as never)
        .mockResolvedValue({
          error: {},
          response: new Response("", { status }),
        } as never);
      render(
        <QueryClientProvider client={client}>
          <PreferencesProvider>
            <SystemDiagnosticsPanel />
          </PreferencesProvider>
        </QueryClientProvider>,
      );

      await user.click(
        await screen.findByRole("button", { name: "Refresh diagnostics" }),
      );
      await screen.findByText("Request failed");
      expect(onAuthFailure).toHaveBeenCalledExactlyOnceWith(status);
      expect(screen.queryByText("gateway-01")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("heading", { name: "Local mount capacity" }),
      ).not.toBeInTheDocument();
      client.clear();
    },
  );
});
