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
  createUserIdentity,
  deleteUserIdentity,
  listIdentityProviders,
  listUserIdentities,
} from "../../../client";
import type { UserIdentity } from "../../../client";
import { AntdProvider } from "../../../app/AntdProvider";
import { PreferencesProvider } from "../../../lib/preferences";
import { TestQueryProvider } from "../../../test/queryClient";
import { UserIdentitiesPanel } from "./UserIdentitiesPanel";

const providerList = (issuer: string) => ({
  items: [
    {
      id: "00000000-0000-0000-0000-000000000201",
      slug: "default",
      kind: "oidc",
      oidc: { issuer },
    },
  ],
});

vi.mock("../../../client", () => ({
  createUserIdentity: vi.fn(),
  deleteUserIdentity: vi.fn(),
  listIdentityProviders: vi.fn(),
  listUserIdentities: vi.fn(),
}));

const mockCreateIdentity = vi.mocked(createUserIdentity);
const mockDeleteIdentity = vi.mocked(deleteUserIdentity);
const mockListProviders = vi.mocked(listIdentityProviders);
const mockListIdentities = vi.mocked(listUserIdentities);

const identity: UserIdentity = {
  id: "00000000-0000-0000-0000-000000000101",
  userId: "00000000-0000-0000-0000-000000000001",
  kind: "oidc",
  providerId: "00000000-0000-0000-0000-000000000201",
  issuer: "https://issuer.example.test",
  subject: "provider-subject",
  email: "alice@example.test",
  displayName: "Alice",
  emailVerified: true,
  createdAt: "2026-08-10T08:00:00Z",
  updatedAt: "2026-08-10T08:00:00Z",
};

function renderPanel() {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <UserIdentitiesPanel userId={identity.userId} />
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("UserIdentitiesPanel", () => {
  it("loads identities and supports linking and unlinking", async () => {
    mockListIdentities.mockResolvedValue({
      data: { items: [identity] },
    } as never);
    mockListProviders.mockResolvedValue({
      data: providerList(identity.issuer),
    } as never);
    mockCreateIdentity.mockResolvedValue({
      data: {
        ...identity,
        id: "00000000-0000-0000-0000-000000000102",
        subject: "second-subject",
      },
    } as never);
    mockDeleteIdentity.mockResolvedValue({} as never);

    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByText("provider-subject")).toBeInTheDocument();
    expect(mockListIdentities).toHaveBeenCalledWith({
      path: { userId: identity.userId },
      signal: expect.any(AbortSignal),
    });

    await user.click(screen.getByText("Link identity", { selector: "span" }));
    expect(screen.getByLabelText("Issuer")).toHaveValue(identity.issuer);
    await user.type(screen.getByLabelText("Subject"), "second-subject");
    const dialog = screen.getByRole("dialog", { name: "Link OIDC identity" });
    await user.click(within(dialog).getByRole("button", { name: /Link/ }));

    await waitFor(() => {
      expect(mockCreateIdentity).toHaveBeenCalledWith({
        path: { userId: identity.userId },
        body: {
          providerId: "00000000-0000-0000-0000-000000000201",
          issuer: "https://issuer.example.test",
          subject: "second-subject",
        },
      });
    });
    expect(await screen.findByText("second-subject")).toBeInTheDocument();

    const unlinkButtons = screen.getAllByRole("button", {
      name: "Unlink identity",
    });
    await user.click(unlinkButtons[0]);
    await user.click(await screen.findByRole("button", { name: /^Unlink$/ }));

    await waitFor(() => {
      expect(mockDeleteIdentity).toHaveBeenCalledWith({
        path: { userId: identity.userId, identityId: identity.id },
      });
    });
    expect(screen.queryByText("provider-subject")).not.toBeInTheDocument();
  });

  it("clears a stale issuer when the OIDC provider is removed", async () => {
    mockListIdentities.mockResolvedValue({ data: { items: [] } } as never);
    mockListProviders
      .mockResolvedValueOnce({ data: providerList(identity.issuer) } as never)
      .mockResolvedValueOnce({ data: { items: [] } } as never);

    const user = userEvent.setup();
    renderPanel();

    await screen.findByText("No external identity linked");
    await user.click(screen.getByText("Link identity", { selector: "span" }));
    expect(screen.getByLabelText("Issuer")).toHaveValue(identity.issuer);
    await user.click(screen.getByRole("button", { name: /Cancel/ }));

    await user.click(
      screen.getByRole("button", { name: "Refresh external identities" }),
    );
    await waitFor(() => expect(mockListProviders).toHaveBeenCalledTimes(2));
    await user.click(screen.getByText("Link identity", { selector: "span" }));

    expect(screen.getByLabelText("Issuer")).toHaveValue("");
    expect(
      screen.getByText(
        "No OIDC provider is configured, so identities cannot be linked yet.",
      ),
    ).toBeInTheDocument();
    await user.type(screen.getByLabelText("Subject"), "provider-subject");
    const dialog = screen.getByRole("dialog", { name: "Link OIDC identity" });
    expect(within(dialog).getByRole("button", { name: /Link/ })).toBeDisabled();
  });

  it("shows the shared loading state while the first request is in flight", async () => {
    mockListIdentities.mockReturnValue(new Promise(() => {}) as never);
    mockListProviders.mockReturnValue(new Promise(() => {}) as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <UserIdentitiesPanel userId="00000000-0000-0000-0000-000000000001" />
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByRole("status")).toBeInTheDocument();
  });

  it("shares the console empty state", async () => {
    mockListIdentities.mockResolvedValue({ data: { items: [] } } as never);
    mockListProviders.mockResolvedValue({
      data: providerList("https://issuer.example.test"),
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <UserIdentitiesPanel userId="00000000-0000-0000-0000-000000000001" />
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    const empty = await screen.findByText("No external identity linked");
    expect(empty.closest(".ag-empty-state")).not.toBeNull();
  });

  it("reports a failed link inside the dialog that started it", async () => {
    mockListIdentities.mockResolvedValue({ data: { items: [] } } as never);
    mockListProviders.mockResolvedValue({
      data: providerList(identity.issuer),
    } as never);
    let rejectLink: (error: unknown) => void = () => {};
    mockCreateIdentity.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectLink = reject;
        }) as never,
    );

    const user = userEvent.setup();
    renderPanel();

    await screen.findByText("No external identity linked");
    await user.click(screen.getByText("Link identity", { selector: "span" }));
    await user.type(screen.getByLabelText("Subject"), "provider-subject");
    const dialog = screen.getByRole("dialog", { name: "Link OIDC identity" });
    await user.click(within(dialog).getByRole("button", { name: /Link/ }));

    rejectLink(new Error("network down"));
    expect(await within(dialog).findByText("network down")).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "Link OIDC identity" }),
    ).toBeVisible();
  });
});
