import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  deleteGrant,
  listAuthorizationRoles,
  listGrants,
  listRepositories,
  listRepositoryGrants,
  upsertGrant,
} from "../../../client";
import { AntdProvider } from "../../../app/AntdProvider";
import { PreferencesProvider } from "../../../lib/preferences";
import { TestQueryProvider } from "../../../test/queryClient";
import { UserRepositoryAccessPanel } from "./UserRepositoryAccessPanel";

vi.mock("../../../client", () => ({
  deleteGrant: vi.fn(),
  listAuthorizationRoles: vi.fn(),
  listGrants: vi.fn(),
  listRepositories: vi.fn(),
  listRepositoryGrants: vi.fn(),
  upsertGrant: vi.fn(),
}));

const mockListRepositoryGrants = vi.mocked(listRepositoryGrants);
const mockListRepositories = vi.mocked(listRepositories);
const mockListAuthorizationRoles = vi.mocked(listAuthorizationRoles);
const mockListGrants = vi.mocked(listGrants);
const mockUpsertGrant = vi.mocked(upsertGrant);
const mockDeleteGrant = vi.mocked(deleteGrant);

const username = "alice";
const principal = `user:${username}`;
const artifactsId = "00000000-0000-0000-0000-0000000000a1";
const iconsId = "00000000-0000-0000-0000-0000000000a2";

const artifacts = {
  id: artifactsId,
  name: "artifacts",
  format: "raw",
  state: "active",
  version: "3",
  repositoryTypes: ["hosted"],
  groupSupported: false,
  anonymousRead: false,
};

const icons = { ...artifacts, id: iconsId, name: "icons", format: "oci" };

const bobAdmin = {
  principal: "user:bob",
  scopes: ["repositories:admin", "repositories:read"],
  resourcePrefix: "builds/",
};

const aliceReleases = {
  principal,
  scopes: ["repositories:write"],
  resourcePrefix: "releases/",
};

const aliceSnapshots = {
  principal,
  scopes: ["repositories:read"],
  resourcePrefix: "snapshots/",
};

function record(overrides: Record<string, unknown> = {}) {
  return {
    repositoryId: artifactsId,
    repositoryName: "artifacts",
    format: "raw",
    principal,
    scopes: ["repositories:read"],
    ...overrides,
  };
}

function renderPanel(accountUsername = username) {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <UserRepositoryAccessPanel username={accountUsername} />
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

function mockOkSources() {
  mockListRepositories.mockResolvedValue({
    data: { items: [artifacts, icons] },
  } as never);
  mockListAuthorizationRoles.mockResolvedValue({ data: [] } as never);
}

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe("UserRepositoryAccessPanel", () => {
  it.each([
    "operator {repositoryName}",
    "operator {username}",
    "operator {{repositoryName}}",
    "operator ${repositoryName}",
    "operator {unknown}",
    "operator $&-$'-$`-$$",
  ])(
    "keeps the literal username in grant-removal confirmation (%s)",
    async (accountUsername) => {
      mockListRepositoryGrants.mockResolvedValue({
        data: [record({ principal: `user:${accountUsername}` })],
      } as never);
      mockOkSources();
      renderPanel(accountUsername);

      await userEvent.click(
        await screen.findByRole("button", { name: "Remove repository grant" }),
      );
      await waitFor(() => {
        expect(
          screen.getByText(
            `After removal, every repository grant for ${accountUsername} on "artifacts" is deleted. Other users are unaffected.`,
          ),
        ).toBeVisible();
      });
      expect(mockDeleteGrant).not.toHaveBeenCalled();
    },
  );

  it("lists only this account's grants with capability and prefix", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({
          scopes: ["repositories:write"],
          resourcePrefix: "releases/",
        }),
        record({
          repositoryId: iconsId,
          repositoryName: "icons",
          format: "oci",
          principal: "user:bob",
          scopes: ["repositories:admin"],
        }),
      ],
    } as never);
    mockOkSources();

    renderPanel();

    expect(await screen.findByText("artifacts")).toBeInTheDocument();
    expect(screen.getByText("Read + write")).toBeInTheDocument();
    expect(screen.getByText("releases/")).toBeInTheDocument();
    expect(screen.queryByText("icons")).not.toBeInTheDocument();
    expect(mockListRepositoryGrants).toHaveBeenCalledTimes(1);
  });

  it("lists every entry of one repository separately so each can be acted on", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
        record({ resourcePrefix: "snapshots/" }),
      ],
    } as never);
    mockOkSources();

    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    expect(screen.getByText("snapshots/")).toBeInTheDocument();
    expect(screen.getAllByText("artifacts")).toHaveLength(2);
    expect(
      screen.getAllByRole("button", { name: "Edit this grant" }),
    ).toHaveLength(2);
    expect(
      screen.getAllByRole("button", { name: "Remove repository grant" }),
    ).toHaveLength(2);
  });

  it("shows the empty state and the load error with a retry", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockOkSources();
    const { unmount } = renderPanel();
    expect(
      await screen.findByText("This user has no repository grants"),
    ).toBeInTheDocument();
    unmount();

    mockListRepositoryGrants.mockResolvedValue({
      error: new Error("boom"),
    } as never);
    renderPanel();
    expect(await screen.findByText("Request failed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry/ })).toBeInTheDocument();
  });

  it("adds a grant through the single-grant upsert, without the repository's set", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({
      data: [{ principal: "user:bob", scopes: ["repositories:write"] }],
    } as never);
    mockUpsertGrant.mockResolvedValue({ data: [] } as never);

    const user = userEvent.setup();
    renderPanel();

    await user.click(await screen.findByRole("button", { name: /Add grant/ }));
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByText("artifacts · raw"));
    expect(await screen.findByText("Read · browse / pull")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Save$/ }));

    await waitFor(() => {
      expect(mockUpsertGrant).toHaveBeenCalledTimes(1);
    });
    expect(mockUpsertGrant).toHaveBeenCalledWith({
      path: { repositoryId: artifactsId },
      body: {
        principal,
        scopes: ["repositories:read"],
        resourcePrefix: undefined,
      },
    });
    // The repository's other grants are never read back into a submission.
    expect(mockDeleteGrant).not.toHaveBeenCalled();
    expect(
      await screen.findByText("Repository grant saved"),
    ).toBeInTheDocument();
  });

  it("prefills the acted-on row and updates it in place when the prefix is unchanged", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({ data: [aliceReleases] } as never);
    mockUpsertGrant.mockResolvedValue({ data: [] } as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit this grant" }));

    expect(
      await screen.findByText("Write · publish / edit"),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue("releases/")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Save$/ }));

    await waitFor(() => {
      expect(mockUpsertGrant).toHaveBeenCalledWith({
        path: { repositoryId: artifactsId },
        body: {
          principal,
          scopes: ["repositories:write"],
          resourcePrefix: "releases/",
        },
      });
    });
    expect(mockDeleteGrant).not.toHaveBeenCalled();
  });

  it("upserts the new prefix and deletes the old row when an edit moves the entry", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
        record({ resourcePrefix: "snapshots/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({
      data: [aliceReleases, aliceSnapshots],
    } as never);
    mockUpsertGrant.mockResolvedValue({ data: [] } as never);
    mockDeleteGrant.mockResolvedValue({} as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(
      screen.getAllByRole("button", { name: "Edit this grant" })[0],
    );
    const prefixInput = await screen.findByDisplayValue("releases/");
    // One change event replaces the whole prefix; clearing first would unmount
    // the prefix fields while the value is empty.
    fireEvent.change(prefixInput, { target: { value: "stable/" } });
    await user.click(screen.getByRole("button", { name: /^Save$/ }));

    // The old row goes first, or the edit would fork one grant into two and a
    // failure would leave the grant wider than asked for; the account's
    // snapshots/ entry is never part of either call.
    await waitFor(() => {
      expect(mockDeleteGrant).toHaveBeenCalledWith({
        path: { repositoryId: artifactsId },
        query: { principal, resourcePrefix: "releases/" },
      });
    });
    await waitFor(() => {
      expect(mockUpsertGrant).toHaveBeenCalledWith({
        path: { repositoryId: artifactsId },
        body: {
          principal,
          scopes: ["repositories:write"],
          resourcePrefix: "stable/",
        },
      });
    });
    expect(mockDeleteGrant.mock.invocationCallOrder[0]).toBeLessThan(
      mockUpsertGrant.mock.invocationCallOrder[0],
    );
  });

  it("does not write the new prefix when removing the old one fails", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({ data: [aliceReleases] } as never);
    mockDeleteGrant.mockResolvedValue({
      error: { status: 500, message: "delete failed" },
    } as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit this grant" }));
    const prefixInput = await screen.findByDisplayValue("releases/");
    fireEvent.change(prefixInput, { target: { value: "stable/" } });
    await user.click(screen.getByRole("button", { name: /^Save$/ }));

    expect(
      await screen.findByText(
        "The previous resource scope could not be removed, so the new grant was not saved. Please retry.",
      ),
    ).toBeInTheDocument();
    expect(mockUpsertGrant).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("keeps the dialog open with the error when the upsert fails", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({ data: [] } as never);
    mockUpsertGrant.mockResolvedValue({
      error: { status: 500, message: "upsert failed" },
    } as never);

    const user = userEvent.setup();
    renderPanel();

    await user.click(await screen.findByRole("button", { name: /Add grant/ }));
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByText("artifacts · raw"));
    await screen.findByText("Read · browse / pull");
    await user.click(screen.getByRole("button", { name: /^Save$/ }));

    expect(await screen.findByText("upsert failed")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("removes every entry of the account on that repository, one delete each", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
        record({ resourcePrefix: "snapshots/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({
      data: [aliceReleases, aliceSnapshots, bobAdmin],
    } as never);
    mockDeleteGrant.mockResolvedValue({} as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(
      screen.getAllByRole("button", { name: "Remove repository grant" })[0],
    );
    await user.click(await screen.findByRole("button", { name: /^Remove$/ }));

    await waitFor(() => {
      expect(mockDeleteGrant).toHaveBeenCalledTimes(2);
    });
    expect(mockDeleteGrant).toHaveBeenNthCalledWith(1, {
      path: { repositoryId: artifactsId },
      query: { principal, resourcePrefix: "releases/" },
    });
    expect(mockDeleteGrant).toHaveBeenNthCalledWith(2, {
      path: { repositoryId: artifactsId },
      query: { principal, resourcePrefix: "snapshots/" },
    });
    // The other principal's entry is never deleted or resubmitted.
    expect(mockUpsertGrant).not.toHaveBeenCalled();
    expect(
      await screen.findByText("Repository grant removed"),
    ).toBeInTheDocument();
  });

  it("tolerates rows a concurrent removal already deleted", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
        record({ resourcePrefix: "snapshots/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({
      data: [aliceReleases, aliceSnapshots],
    } as never);
    mockDeleteGrant
      .mockResolvedValueOnce({
        error: { status: 404, code: "not_found", message: "gone" },
      } as never)
      .mockResolvedValueOnce({} as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(
      screen.getAllByRole("button", { name: "Remove repository grant" })[0],
    );
    await user.click(await screen.findByRole("button", { name: /^Remove$/ }));

    // The already-gone row is skipped and the remaining row is still removed.
    await waitFor(() => {
      expect(mockDeleteGrant).toHaveBeenCalledTimes(2);
    });
    expect(
      await screen.findByText("Repository grant removed"),
    ).toBeInTheDocument();
  });

  it("warns before an edit moves the entry onto a prefix that already exists", async () => {
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        record({ scopes: ["repositories:write"], resourcePrefix: "releases/" }),
        record({ resourcePrefix: "snapshots/" }),
      ],
    } as never);
    mockOkSources();
    mockListGrants.mockResolvedValue({
      data: [aliceReleases, aliceSnapshots],
    } as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("releases/")).toBeInTheDocument();
    await user.click(
      screen.getAllByRole("button", { name: "Edit this grant" })[0],
    );

    expect(
      screen.queryByText(
        "This user already holds a grant at this resource scope; saving replaces it.",
      ),
    ).not.toBeInTheDocument();

    // One change event replaces the whole prefix; clearing first would unmount
    // the prefix fields while the value is empty.
    fireEvent.change(await screen.findByDisplayValue("releases/"), {
      target: { value: "snapshots/" },
    });

    expect(
      await screen.findByText(
        "This user already holds a grant at this resource scope; saving replaces it.",
      ),
    ).toBeInTheDocument();
  });
});
