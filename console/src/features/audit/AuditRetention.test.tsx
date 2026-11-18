import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getAuditRetentionPolicy, listAuditRetentionJobs } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AuditRetentionPage } from "./AuditRetention";

vi.mock("../../client", () => ({
  executeAuditRetention: vi.fn(),
  getAuditRetentionPolicy: vi.fn(),
  listAuditRetentionJobs: vi.fn(),
  replaceAuditRetentionPolicy: vi.fn(),
}));

const mockPolicy = vi.mocked(getAuditRetentionPolicy);
const mockJobs = vi.mocked(listAuditRetentionJobs);

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.clearAllMocks();
});

function renderPage() {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AuditRetentionPage />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

describe("AuditRetentionPage", () => {
  it("keeps policy controls clear when there are unsaved changes", async () => {
    const user = userEvent.setup();
    mockPolicy.mockResolvedValue({
      data: { version: "3", enabled: true, keepDays: 90 },
    } as never);
    mockJobs.mockResolvedValue({ data: [] } as never);

    renderPage();

    expect(await screen.findByText("Policy settings")).toBeInTheDocument();
    expect(screen.getByText("Cleanup jobs (0)")).toBeInTheDocument();
    expect(screen.queryByText("Cleanup note")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("group", { name: "Page summary" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled();

    await user.click(
      screen.getByRole("switch", { name: /Toggle automatic cleanup/ }),
    );

    expect(screen.getByRole("button", { name: "Save policy" })).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Run cleanup now" }),
    ).toBeDisabled();
  });
});
