import { MemoryRouter } from "react-router-dom";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  addUserGroupMember,
  createUserGroup,
  deleteUserGroup,
  listIdentityProviders,
  listUserGroupMembers,
  listUserGroups,
  listUsers,
  removeUserGroupMember,
  renameUserGroup,
  setUserGroupGlobalRole,
} from "../../../client";
import { AntdProvider } from "../../../app/AntdProvider";
import { PreferencesProvider } from "../../../lib/preferences";
import { TestQueryProvider } from "../../../test/queryClient";
import { UserGroupsPage } from "./UserGroups";

vi.mock("../../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../../client")>("../../../client");
  return {
    ...actual,
    addUserGroupMember: vi.fn(),
    createUserGroup: vi.fn(),
    deleteUserGroup: vi.fn(),
    listIdentityProviders: vi.fn(),
    listUserGroupMembers: vi.fn(),
    listUserGroups: vi.fn(),
    listUsers: vi.fn(),
    removeUserGroupMember: vi.fn(),
    renameUserGroup: vi.fn(),
    setUserGroupGlobalRole: vi.fn(),
  };
});

const stamp = "2026-11-01T00:00:00Z";
const local = {
  id: "00000000-0000-4000-8000-000000000001",
  displayName: "release-managers",
  source: "local" as const,
  globalRole: "none" as const,
  version: "v1",
  createdAt: stamp,
  updatedAt: stamp,
};
const claims = {
  ...local,
  id: "00000000-0000-4000-8000-000000000002",
  displayName: "entra-admins",
  source: "claims" as const,
  providerId: "00000000-0000-4000-9000-000000000001",
  externalId: "g-1",
};
const alice = {
  id: "00000000-0000-4000-a000-000000000001",
  name: "alice",
  state: "active",
  role: "member",
};
const bob = {
  ...alice,
  id: "00000000-0000-4000-a000-000000000002",
  name: "bob",
};

function mockReads() {
  vi.mocked(listUserGroups).mockResolvedValue({
    data: { items: [local, claims], limit: 100 },
  } as never);
  vi.mocked(listUserGroupMembers).mockResolvedValue({
    data: {
      items: [
        {
          userId: alice.id,
          userName: "alice",
          source: "local",
          createdAt: stamp,
        },
      ],
      limit: 100,
    },
  } as never);
  vi.mocked(listUsers).mockResolvedValue({
    data: { items: [alice, bob] },
  } as never);
  vi.mocked(listIdentityProviders).mockResolvedValue({
    data: {
      items: [
        {
          id: claims.providerId,
          slug: "entra",
          kind: "oidc",
          displayName: "Entra ID",
        },
      ],
    },
  } as never);
}

function renderPage() {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <UserGroupsPage />
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

async function rowOf(name: string) {
  const cell = await screen.findByText(name);
  return within(cell.closest("tr") as HTMLElement);
}

async function choose(index: number, option: string) {
  const dialog = screen.getByRole("dialog");
  await userEvent.click(within(dialog).getAllByRole("combobox")[index]);
  await userEvent.click(await screen.findByTitle(option));
}

describe("User groups page", () => {
  it("lists groups with their source and global role", async () => {
    mockReads();
    renderPage();
    const row = await rowOf("release-managers");
    expect(row.getByText("Local")).toBeVisible();
    const claimsRow = await rowOf("entra-admins");
    expect(claimsRow.getByText("Sign-in claims")).toBeVisible();
  });

  it("shows the empty state", async () => {
    vi.mocked(listUserGroups).mockResolvedValue({
      data: { items: [], limit: 100 },
    } as never);
    renderPage();
    expect(await screen.findByText("No user groups yet")).toBeVisible();
  });

  it("creates a local group", async () => {
    mockReads();
    vi.mocked(createUserGroup).mockResolvedValue({ data: local } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /New group/ }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox"), " ops ");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create" }),
    );
    await waitFor(() =>
      expect(createUserGroup).toHaveBeenCalledWith({
        body: { displayName: "ops", source: "local" },
      }),
    );
  });

  it("creates a claims group for a provider", async () => {
    mockReads();
    vi.mocked(createUserGroup).mockResolvedValue({ data: claims } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /New group/ }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.type(
      within(dialog).getAllByRole("textbox")[0],
      "entra-ops",
    );
    await choose(0, "Sign-in claims");
    await choose(1, "Entra ID");
    await userEvent.type(within(dialog).getAllByRole("textbox")[1], "g-9");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create" }),
    );
    await waitFor(() =>
      expect(createUserGroup).toHaveBeenCalledWith({
        body: {
          displayName: "entra-ops",
          source: "claims",
          providerId: claims.providerId,
          externalId: "g-9",
        },
      }),
    );
  });

  it("renames with the version it read", async () => {
    mockReads();
    vi.mocked(renameUserGroup).mockResolvedValue({ data: local } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("release-managers")).getByRole("button", { name: "Rename" }),
    );
    const dialog = screen.getByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await userEvent.clear(input);
    await userEvent.type(input, "releasers");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(renameUserGroup).toHaveBeenCalledWith({
        path: { groupId: local.id },
        headers: { "If-Match": "v1" },
        body: { displayName: "releasers" },
      }),
    );
  });

  it("warns before granting the administrator role", async () => {
    mockReads();
    vi.mocked(setUserGroupGlobalRole).mockResolvedValue({
      data: local,
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("release-managers")).getByRole("button", {
        name: "Global role",
      }),
    );
    await choose(0, "Administrator");
    expect(
      screen.getByText("Every member becomes an administrator"),
    ).toBeInTheDocument();
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Save" }),
    );
    await waitFor(() =>
      expect(setUserGroupGlobalRole).toHaveBeenCalledWith({
        path: { groupId: local.id },
        headers: { "If-Match": "v1" },
        body: { globalRole: "admin" },
      }),
    );
  });

  it("keeps the global role of a claims group at none", async () => {
    mockReads();
    renderPage();
    await userEvent.click(
      (await rowOf("entra-admins")).getByRole("button", {
        name: "Global role",
      }),
    );
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(/only ever has the global role none/),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("deletes after confirmation", async () => {
    mockReads();
    vi.mocked(deleteUserGroup).mockResolvedValue({ data: undefined } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("release-managers")).getByRole("button", { name: "Delete" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );
    await waitFor(() =>
      expect(deleteUserGroup).toHaveBeenCalledWith({
        path: { groupId: local.id },
      }),
    );
  });

  it("adds and removes members", async () => {
    mockReads();
    vi.mocked(addUserGroupMember).mockResolvedValue({
      data: undefined,
    } as never);
    vi.mocked(removeUserGroupMember).mockResolvedValue({
      data: undefined,
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("release-managers")).getByRole("button", {
        name: "Members",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("alice")).toBeInTheDocument();
    await choose(0, "bob");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Add member" }),
    );
    await waitFor(() =>
      expect(addUserGroupMember).toHaveBeenCalledWith({
        path: { groupId: local.id },
        body: { userId: bob.id },
      }),
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Remove" }),
    );
    const confirm = screen.getAllByRole("dialog").at(-1) as HTMLElement;
    await userEvent.click(
      within(confirm).getByRole("button", { name: "Remove" }),
    );
    await waitFor(() =>
      expect(removeUserGroupMember).toHaveBeenCalledWith({
        path: { groupId: local.id, userId: alice.id },
      }),
    );
  });
});
