import { useState } from "react";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { getGroupResolution } from "../../../client";
import type { Group, GroupResolution } from "../../../client";
import { PreferencesProvider } from "../../../lib/preferences";
import { GroupResolutionDialog } from "./GroupResolutionDialog";
import { TestQueryProvider } from "../../../test/queryClient";

vi.mock("../../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../client")>()),
  getGroupResolution: vi.fn(),
}));
const getResolution = vi.mocked(getGroupResolution);
const group: Group = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "raw-chain",
  format: "raw",
  visibility: "private",
  members: [],
  version: "1",
};
const plan: GroupResolution = {
  groupId: group.id,
  groupVersion: "1",
  format: "raw",
  strategy: "hosted_first",
  excludedMemberCount: 1,
  members: [
    {
      repositoryId: "22222222-2222-4222-8222-222222222222",
      repositoryName: "hosted-from-server",
      type: "hosted",
      configuredPosition: 3,
      resolutionOrder: 1,
    },
    {
      repositoryId: "33333333-3333-4333-8333-333333333333",
      repositoryName: "proxy-from-server",
      type: "proxy",
      configuredPosition: 0,
      resolutionOrder: 2,
    },
  ],
};
/** Opens the dialog the way the groups page does: mounted while open. */
function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Resolution order
      </button>
      {open && (
        <GroupResolutionDialog
          open={open}
          group={group}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

function showDialog() {
  render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <Harness />
        </PreferencesProvider>
      </TestQueryProvider>
    </MemoryRouter>,
  );
}
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("GroupResolutionDialog", () => {
  it("shows server order, source names and configured positions without inferring a hit", async () => {
    const user = userEvent.setup();
    getResolution.mockResolvedValue({ data: plan } as never);
    showDialog();
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    const rows = await screen.findAllByTestId("resolution-member");
    expect(
      within(rows[0]).getByRole("link", { name: "hosted-from-server" }),
    ).toHaveAttribute("href", `/repositories/${plan.members[0].repositoryId}`);
    expect(
      within(rows[0]).getByText("Configured position 4"),
    ).toBeInTheDocument();
    expect(
      within(rows[1]).getByText("Configured position 1"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "1 configured member(s) are currently excluded from resolution.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Hosted members come first; members of the same type follow their configured positions\. These are candidates, while actual hits depend on the artifact, read permissions and protocol rules\./,
      ),
    ).toBeInTheDocument();
    expect(getResolution).toHaveBeenCalledWith({
      path: { groupId: group.id },
      signal: expect.any(AbortSignal),
    });
  });

  it("retries an initial rejected request without leaving loading below the error", async () => {
    const user = userEvent.setup();
    getResolution
      .mockRejectedValueOnce(new Error("resolution unavailable"))
      .mockResolvedValueOnce({ data: plan } as never);
    showDialog();
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    expect(
      await screen.findByText("resolution unavailable"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Retry/ }));
    expect(await screen.findByText("hosted-from-server")).toBeInTheDocument();
  });

  it("preserves the current members when refresh fails", async () => {
    const user = userEvent.setup();
    getResolution
      .mockResolvedValueOnce({ data: plan } as never)
      .mockRejectedValueOnce(new Error("refresh unavailable"));
    showDialog();
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    await screen.findByText("hosted-from-server");
    await user.click(screen.getByRole("button", { name: /Refresh/ }));
    expect(await screen.findByText("refresh unavailable")).toBeInTheDocument();
    expect(screen.getByText("hosted-from-server")).toBeInTheDocument();
  });

  it("renders an empty result without an active loading state", async () => {
    const user = userEvent.setup();
    getResolution.mockResolvedValueOnce({
      data: { ...plan, members: [] },
    } as never);
    showDialog();
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    expect(await screen.findByText("No eligible members")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("ignores a response from an earlier opening", async () => {
    const user = userEvent.setup();
    let finish: ((value: never) => void) | undefined;
    getResolution
      .mockImplementationOnce(
        () =>
          new Promise<never>((resolve) => {
            finish = resolve;
          }),
      )
      .mockResolvedValueOnce({ data: plan } as never);
    showDialog();
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    await user.click(screen.getAllByRole("button", { name: "Close" })[0]);
    await user.click(screen.getByRole("button", { name: "Resolution order" }));
    await screen.findByText("hosted-from-server");
    finish?.({ data: { ...plan, members: [] } } as never);
    await waitFor(() =>
      expect(screen.getAllByTestId("resolution-member")).toHaveLength(2),
    );
  });
});
