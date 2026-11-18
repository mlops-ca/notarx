import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getArtifactQuarantine, replaceArtifactQuarantine } from "../../client";
import type { ArtifactQuarantine } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { ArtifactQuarantinePanel } from "./ArtifactQuarantinePanel";

vi.mock("../../client", () => ({
  getArtifactQuarantine: vi.fn(),
  replaceArtifactQuarantine: vi.fn(),
}));

const mockGetQuarantine = vi.mocked(getArtifactQuarantine);
const mockReplaceQuarantine = vi.mocked(replaceArtifactQuarantine);
const digest = `sha256:${"a".repeat(64)}`;

function quarantine(
  overrides: Partial<ArtifactQuarantine> = {},
): ArtifactQuarantine {
  return {
    repositoryId: "repo-1",
    format: "raw",
    coordinate: "releases/widget.bin",
    digest,
    state: "quarantined",
    reason: "critical vulnerability under investigation",
    version: "1",
    updatedBy: "alice",
    updatedAt: "2026-08-11T08:00:00Z",
    quarantinedAt: "2026-08-11T08:00:00Z",
    ...overrides,
  };
}

function renderPanel(canManage = true) {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <ArtifactQuarantinePanel
          repositoryId="repo-1"
          coordinate="releases/widget.bin"
          digest={digest}
          canManage={canManage}
        />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.clearAllMocks();
});

describe("ArtifactQuarantinePanel", () => {
  it("shows active state read-only without management permission", async () => {
    mockGetQuarantine.mockResolvedValue({ data: quarantine() } as never);
    renderPanel(false);

    expect(await screen.findByText("Artifact quarantined")).toBeInTheDocument();
    expect(mockGetQuarantine).toHaveBeenCalledOnce();
    expect(
      screen.queryByRole("button", { name: /Release/ }),
    ).not.toBeInTheDocument();
  });

  it("reads quarantine as an ongoing state, not a system error", async () => {
    mockGetQuarantine.mockResolvedValue({ data: quarantine() } as never);
    renderPanel();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveClass("ant-alert-warning");
    expect(alert).not.toHaveClass("ant-alert-error");
  });

  it("treats 404 as unquarantined and creates the first record at version zero", async () => {
    const user = userEvent.setup();
    const created = quarantine();
    mockGetQuarantine
      .mockResolvedValueOnce({
        error: { status: 404, code: "not_found" },
      } as never)
      .mockResolvedValueOnce({ data: created } as never);
    mockReplaceQuarantine.mockResolvedValue({ data: created } as never);

    renderPanel();

    await user.click(await screen.findByRole("button", { name: /Quarantine/ }));
    const confirm = screen
      .getAllByRole("button", { name: /Quarantine/ })
      .at(-1)!;
    expect(confirm).toBeDisabled();

    await user.type(
      screen.getByLabelText("Quarantine reason"),
      "  critical CVE  ",
    );
    await user.click(confirm);

    await waitFor(() =>
      expect(mockReplaceQuarantine).toHaveBeenCalledWith({
        path: { repositoryId: "repo-1" },
        query: { coordinate: "releases/widget.bin", digest },
        headers: { "If-Match": "0" },
        body: { state: "quarantined", reason: "critical CVE" },
      }),
    );
    await waitFor(() => expect(mockGetQuarantine).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("Artifact quarantined")).toBeInTheDocument();
  });

  it("uses the current version and keeps transition errors inside the modal", async () => {
    const user = userEvent.setup();
    mockGetQuarantine.mockResolvedValue({
      data: quarantine({ version: "7" }),
    } as never);
    mockReplaceQuarantine.mockResolvedValue({
      error: {
        status: 412,
        code: "version_conflict",
        message: "quarantine version changed",
      },
    } as never);

    renderPanel();

    expect(await screen.findByText("Artifact quarantined")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: /Release/ }).at(0)!);
    await user.type(
      screen.getByLabelText("Release reason"),
      "review completed",
    );
    await user.click(
      screen.getAllByRole("button", { name: /Release/ }).at(-1)!,
    );

    await waitFor(() =>
      expect(mockReplaceQuarantine).toHaveBeenCalledWith(
        expect.objectContaining({
          headers: { "If-Match": "7" },
          body: { state: "released", reason: "review completed" },
        }),
      ),
    );
    expect(await screen.findByText("Request failed")).toBeInTheDocument();
    expect(screen.getByText("quarantine version changed")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
