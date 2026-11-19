import { MemoryRouter } from "react-router-dom";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import {
  createGroup,
  deleteGroup,
  getGroupCapacity,
  listFormatProfiles,
  listGroupMembers,
  listGroups,
  listRepositories,
  replaceGroup,
  replaceGroupMembers,
} from "../../client";
import type { FormatProfile, Group, Repository } from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { resetFormatProfilesCacheForTests } from "../../lib/formatProfiles";
import { GroupsPage } from "./Groups";
import { TestQueryProvider } from "../../test/queryClient";

vi.mock("../../client", () => ({
  createGroup: vi.fn(),
  deleteGroup: vi.fn(),
  getGroupCapacity: vi.fn(),
  listFormatProfiles: vi.fn(),
  listGroupMembers: vi.fn(),
  listGroups: vi.fn(),
  listRepositories: vi.fn(),
  replaceGroup: vi.fn(),
  replaceGroupMembers: vi.fn(),
}));

const group: Group = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "oci-mirror",
  format: "oci",
  visibility: "private",
  members: [],
  version: "2",
};

const repository: Repository = {
  id: "22222222-2222-4222-8222-222222222222",
  name: "npm-lib",
  format: "oci",
  type: "hosted",
  allowedHosts: [],
  visibility: "private",
  mavenStrictPublication: false,
  state: "active",
  version: "1",
};

const profiles: FormatProfile[] = [
  {
    format: "oci",
    repositoryTypes: ["hosted", "proxy"],
    groupSupported: true,
    publicVisibility: true,
    hostedOperations: ["read", "publish"],
    proxyOperations: ["read"],
  },
];

// The mask is the dismissal route these dialogs' `busy` flag controls, and it
// only closes on a click that both starts and lands on it.
function clickMask() {
  const mask = document.querySelector(".ant-modal-wrap");
  expect(mask).not.toBeNull();
  fireEvent.mouseDown(mask as HTMLElement);
  fireEvent.click(mask as HTMLElement);
}

function renderPage(groups: Group[] = [group]) {
  vi.mocked(listGroups).mockResolvedValue({
    data: { items: groups },
  } as never);
  vi.mocked(listRepositories).mockResolvedValue({
    data: { items: [repository] },
  } as never);
  vi.mocked(listFormatProfiles).mockResolvedValue({
    data: { items: profiles },
  } as never);
  vi.mocked(getGroupCapacity).mockResolvedValue({ data: {} } as never);

  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <GroupsPage />
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>
    </MemoryRouter>,
  );
}

function pendingSave(mock: Mock) {
  let rejectSave: (error: unknown) => void = () => {};
  mock.mockImplementationOnce(
    () =>
      new Promise((_, reject) => {
        rejectSave = reject;
      }),
  );
  return (error: unknown) => rejectSave(error);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  resetFormatProfilesCacheForTests();
});

describe("GroupsPage dialogs", () => {
  it("waits for the complete member read before allowing edits or saves", async () => {
    const user = userEvent.setup();
    let finish: ((value: never) => void) | undefined;
    vi.mocked(listGroupMembers).mockImplementationOnce(
      () =>
        new Promise<never>((resolve) => {
          finish = resolve;
        }),
    );
    vi.mocked(replaceGroupMembers).mockResolvedValue({ data: [] } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /Edit members/ }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: /Save/ })).toBeDisabled();
    expect(
      within(dialog).queryByRole("button", { name: /npm-lib/ }),
    ).not.toBeInTheDocument();
    finish?.({
      data: [{ repositoryId: repository.id, position: 0 }],
      response: new Response(null, { headers: { ETag: '"7"' } }),
    } as never);
    await within(dialog).findByText("npm-lib");
    await user.click(within(dialog).getByRole("button", { name: /Save/ }));
    expect(replaceGroupMembers).toHaveBeenCalledWith({
      path: { groupId: group.id },
      body: [{ repositoryId: repository.id, position: 0 }],
      headers: { "If-Match": "7" },
    });
  });

  it("keeps a failed member read out of the editor until retry succeeds", async () => {
    const user = userEvent.setup();
    vi.mocked(listGroupMembers)
      .mockResolvedValueOnce({
        error: { status: 503, message: "members unavailable" },
      } as never)
      .mockResolvedValueOnce({
        data: [{ repositoryId: repository.id, position: 0 }],
      } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /Edit members/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("members unavailable");
    expect(within(dialog).queryByRole("status")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Save/ })).toBeDisabled();
    expect(
      within(dialog).queryByRole("button", { name: /npm-lib/ }),
    ).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: /Retry/ }));
    await within(dialog).findByText("npm-lib");
    expect(within(dialog).getByRole("button", { name: /Save/ })).toBeEnabled();
    expect(within(dialog).queryByText("members unavailable")).toBeNull();
  });

  it("reopens with fresh members and cancels a read from a closed editor", async () => {
    const user = userEvent.setup();
    let finish: ((value: never) => void) | undefined;
    let oldSignal: AbortSignal | undefined;
    vi.mocked(listGroupMembers)
      .mockResolvedValueOnce({
        data: [{ repositoryId: repository.id, position: 0 }],
      } as never)
      .mockImplementationOnce((options) => {
        oldSignal = options.signal ?? undefined;
        return new Promise<never>((resolve) => {
          finish = resolve;
        });
      })
      .mockResolvedValueOnce({
        data: [{ repositoryId: repository.id, position: 0 }],
        response: new Response(null, { headers: { ETag: '"9"' } }),
      } as never);
    vi.mocked(replaceGroupMembers).mockResolvedValue({ data: [] } as never);
    renderPage();
    const trigger = await screen.findByRole("button", { name: /Edit members/ });
    await user.click(trigger);
    await screen.findByText("npm-lib");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(trigger);
    expect(screen.getByRole("button", { name: /Save/ })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(oldSignal?.aborted).toBe(true));
    await user.click(trigger);
    await screen.findByText("npm-lib");
    finish?.({ data: [] } as never);
    await user.click(screen.getByRole("button", { name: /Save/ }));
    expect(replaceGroupMembers).toHaveBeenCalledWith({
      path: { groupId: group.id },
      body: [{ repositoryId: repository.id, position: 0 }],
      headers: { "If-Match": "9" },
    });
  });

  it("restores row focus after cancelling delete from a focused menu item", async () => {
    const user = userEvent.setup();
    renderPage();
    const trigger = await screen.findByRole("button", {
      name: "More actions for oci-mirror",
    });
    await user.click(trigger);
    const item = await screen.findByRole("menuitem", { name: "Delete group" });
    item.focus();
    expect(item).toHaveFocus();
    await user.click(item);
    await screen.findByRole("dialog");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("recovers format profiles on retry after the service recovers", async () => {
    const user = userEvent.setup();
    vi.mocked(listFormatProfiles).mockResolvedValueOnce({
      error: {
        status: 503,
        message: "format profiles temporarily unavailable",
      },
    } as never);
    renderPage();
    await screen.findByText("format profiles temporarily unavailable");
    expect(screen.getByRole("button", { name: /New group/ })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: /Retry/ }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /New group/ })).toBeEnabled(),
    );
    expect(
      screen.queryByText("format profiles temporarily unavailable"),
    ).not.toBeInTheDocument();
  });

  it("holds the create dialog shut while saving and frees it after a failure", async () => {
    const user = userEvent.setup();
    const fail = pendingSave(vi.mocked(createGroup));

    renderPage();
    await user.click(await screen.findByRole("button", { name: /New group/ }));
    const dialog = await screen.findByRole("dialog");
    await user.type(
      screen.getByRole("textbox", { name: "Group name" }),
      "js-utils",
    );
    await user.click(within(dialog).getByRole("button", { name: /npm-lib/ }));
    await user.click(within(dialog).getByRole("button", { name: /Create/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fail(new Error("network down"));
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("holds the settings dialog shut while saving and frees it after a failure", async () => {
    const user = userEvent.setup();
    const fail = pendingSave(vi.mocked(replaceGroup));

    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /More actions for .+$/ }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Settings" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /Save/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fail(new Error("network down"));
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("holds the member dialog shut while saving and frees it after a failure", async () => {
    const user = userEvent.setup();
    vi.mocked(listGroupMembers).mockResolvedValue({ data: [] } as never);
    const fail = pendingSave(vi.mocked(replaceGroupMembers));

    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /Edit members/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /Save/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fail(new Error("network down"));
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("frees the delete confirmation when the request rejects", async () => {
    const user = userEvent.setup();
    vi.mocked(deleteGroup).mockRejectedValueOnce(new Error("network down"));

    renderPage();
    await user.click(
      await screen.findByRole("button", {
        name: "More actions for oci-mirror",
      }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "Delete group" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /^Delete$/ }));

    expect(await screen.findByText("network down")).toBeInTheDocument();
    // The rejection released `deleting`, so the dialog is dismissible again.
    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });
});

describe("GroupsPage lenient warning", () => {
  it("warns about members that callers of the group cannot see", async () => {
    renderPage([
      {
        ...group,
        visibility: "public",
        lessVisibleMembers: [
          {
            repositoryId: repository.id,
            name: "npm-lib",
            visibility: "private",
          },
        ],
      },
    ]);
    const warning = await screen.findByText(/do not see npm-lib/);
    expect(warning.getAttribute("data-group-less-visible")).toBe(group.id);
  });

  it("shows no warning when every member is at least as visible as the group", async () => {
    renderPage();
    await screen.findByText("oci-mirror");
    expect(document.querySelector("[data-group-less-visible]")).toBeNull();
  });
});
