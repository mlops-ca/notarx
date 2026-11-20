import { MemoryRouter } from "react-router-dom";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createServiceAccount,
  createServiceAccountCredential,
  listServiceAccountCredentials,
  listServiceAccounts,
  revokeServiceAccountCredential,
  updateServiceAccount,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { ServiceAccountsPage } from "./ServiceAccounts";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    createServiceAccount: vi.fn(),
    createServiceAccountCredential: vi.fn(),
    listServiceAccounts: vi.fn(),
    listServiceAccountCredentials: vi.fn(),
    // The allow-list card of the detail reads it; these tests are about the credentials.
    listServiceAccountAssertionAudiences: vi
      .fn()
      .mockResolvedValue({ data: { items: [] } }),
    revokeServiceAccountCredential: vi.fn(),
    updateServiceAccount: vi.fn(),
  };
});

const accountId = "11111111-1111-4111-8111-111111111111";
const oldCredentialId = "22222222-2222-4222-8222-222222222222";
const newCredentialId = "33333333-3333-4333-8333-333333333333";
const activeAccount = {
  id: accountId,
  name: "pipeone-ci",
  description: "Publishes PipeOne release artifacts",
  state: "active" as const,
  createdAt: "2026-08-18T00:00:00Z",
  updatedAt: "2026-08-18T00:00:00Z",
  version: "3",
};
const disabledAccount = {
  ...activeAccount,
  state: "disabled" as const,
  version: "4",
};
const secondAccount = {
  ...activeAccount,
  id: "44444444-4444-4444-8444-444444444444",
  name: "release-bot",
};
const oldCredential = {
  id: oldCredentialId,
  serviceAccountId: accountId,
  name: "jenkins-blue",
  createdAt: "2026-08-18T00:00:00Z",
  expiresAt: "2099-11-16T00:00:00Z",
};
const newCredential = {
  id: newCredentialId,
  serviceAccountId: accountId,
  name: "jenkins-green",
  createdAt: "2026-08-18T01:00:00Z",
  expiresAt: "2099-11-16T01:00:00Z",
};

const mockListServiceAccounts = vi.mocked(listServiceAccounts);
const mockListServiceAccountCredentials = vi.mocked(
  listServiceAccountCredentials,
);
const mockCreateServiceAccount = vi.mocked(createServiceAccount);
const mockCreateServiceAccountCredential = vi.mocked(
  createServiceAccountCredential,
);
const mockRevokeServiceAccountCredential = vi.mocked(
  revokeServiceAccountCredential,
);
const mockUpdateServiceAccount = vi.mocked(updateServiceAccount);

function renderPage() {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <ServiceAccountsPage />
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

describe("service account read containment", () => {
  it.each([null, [null]])(
    "keeps a malformed credential page local and retries the read (%j)",
    async (items) => {
      mockListServiceAccounts.mockResolvedValue({
        data: { items: [activeAccount] },
      } as never);
      mockListServiceAccountCredentials.mockResolvedValue({
        data: { items },
      } as never);
      const errors = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        renderPage();
        expect(
          await screen.findByText(
            "Invalid service account credentials response",
          ),
        ).toBeVisible();
        expect(
          screen.getByRole("button", { name: /New service account/ }),
        ).toBeVisible();
        expect(
          screen.queryByText("Loading credentials…"),
        ).not.toBeInTheDocument();
        mockListServiceAccountCredentials.mockResolvedValue({
          data: { items: [oldCredential] },
        } as never);
        fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
        expect(await screen.findByText(oldCredential.name)).toBeVisible();
        expect(mockListServiceAccountCredentials).toHaveBeenCalledTimes(2);
        expect(errors).not.toHaveBeenCalled();
      } finally {
        errors.mockRestore();
      }
    },
  );

  it("retains verified credential rows when a refresh returns a malformed page", async () => {
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [activeAccount] },
    } as never);
    mockListServiceAccountCredentials
      .mockResolvedValueOnce({ data: { items: [oldCredential] } } as never)
      .mockResolvedValueOnce({ data: { items: null } } as never)
      .mockResolvedValueOnce({ data: { items: [newCredential] } } as never);
    renderPage();
    expect(await screen.findByText(oldCredential.name)).toBeVisible();

    await userEvent.click(screen.getByRole("button", { name: /Refresh/ }));
    expect(
      await screen.findByText("Invalid service account credentials response"),
    ).toBeVisible();
    expect(screen.getByText(oldCredential.name)).toBeVisible();
    expect(screen.queryByText("Loading credentials…")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(await screen.findByText(newCredential.name)).toBeVisible();
    expect(screen.queryByText(oldCredential.name)).not.toBeInTheDocument();
    expect(mockListServiceAccountCredentials).toHaveBeenCalledTimes(3);
  });

  it.each(["ci-$&", "ci-$'", "ci-$`", "ci-$$"])(
    "preserves the literal credential name in revoke controls (%s)",
    async (name) => {
      mockListServiceAccounts.mockResolvedValue({
        data: { items: [activeAccount] },
      } as never);
      mockListServiceAccountCredentials.mockResolvedValue({
        data: { items: [{ ...oldCredential, name }] },
      } as never);
      renderPage();

      const revoke = await screen.findByRole("button", {
        name: `Revoke ${name}`,
      });
      await userEvent.click(revoke);
      await waitFor(() =>
        expect(
          screen.getByText(
            `After revoking ${name}, jobs using that token fail authentication immediately. Other credentials remain valid.`,
          ),
        ).toBeVisible(),
      );
      expect(mockRevokeServiceAccountCredential).not.toHaveBeenCalled();
    },
  );
});

// The mask is the dismissal route the dialogs' `busy` flag controls, and it
// only closes on a click that both starts and lands on it.
function clickMask() {
  const mask = document.querySelector(".ant-modal-wrap");
  expect(mask).not.toBeNull();
  fireEvent.mouseDown(mask as HTMLElement);
  fireEvent.click(mask as HTMLElement);
}

describe("ServiceAccountsPage", () => {
  it("does not present a failed initial request as still loading", async () => {
    mockListServiceAccounts.mockResolvedValue({
      error: { message: "service account inventory unavailable" },
    } as never);

    renderPage();

    expect(await screen.findByText("Request failed")).toBeInTheDocument();
    expect(
      screen.getByText("service account inventory unavailable"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Loading service accounts…"),
    ).not.toBeInTheDocument();
  });

  it("turns a rejected initial request into a recoverable page error", async () => {
    mockListServiceAccounts.mockRejectedValue(
      new Error("service account request rejected"),
    );

    renderPage();

    expect(await screen.findByText("Request failed")).toBeInTheDocument();
    expect(
      screen.getByText("service account request rejected"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry/ })).toBeInTheDocument();
  });

  it("explains when the connected Gateway does not expose service accounts", async () => {
    mockListServiceAccounts.mockResolvedValue({
      error: "404 page not found",
    } as never);

    renderPage();

    expect(
      await screen.findByText(
        "The connected Gateway does not expose this endpoint. The Console and Gateway versions may not match; update or restart Gateway and retry.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry/ })).toBeInTheDocument();
  });

  it("presents one stable machine principal separately from its rotating credentials", async () => {
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [activeAccount] },
    } as never);
    mockListServiceAccountCredentials.mockResolvedValue({
      data: { items: [oldCredential] },
    } as never);

    renderPage();

    expect(await screen.findAllByText("pipeone-ci")).toHaveLength(2);
    expect(
      screen.getByText("Machine identity is separate from credentials"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(`service-account:${accountId}`),
    ).toBeInTheDocument();
    expect(await screen.findByText("jenkins-blue")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Issue credential/ }),
    ).toBeEnabled();
    // Rotation is verified where the principal is granted and used.
    const principal = encodeURIComponent(`service-account:${accountId}`);
    expect(
      screen.getByRole("link", { name: "View repository grants" }),
    ).toHaveAttribute("href", `/access?principal=${principal}`);
    expect(
      screen.getByRole("link", { name: "View recent audits" }),
    ).toHaveAttribute("href", `/audits?actor=${principal}`);
  });

  it("loads additional machine principals and credential history through bounded pages", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts
      .mockResolvedValueOnce({
        data: { items: [activeAccount], nextPageToken: "accounts-page-2" },
      } as never)
      .mockResolvedValueOnce({
        data: { items: [secondAccount] },
      } as never);
    mockListServiceAccountCredentials
      .mockResolvedValueOnce({
        data: { items: [oldCredential], nextPageToken: "credentials-page-2" },
      } as never)
      .mockResolvedValueOnce({
        data: { items: [newCredential] },
      } as never);

    renderPage();

    await user.click(
      await screen.findByRole("button", { name: "Load more accounts" }),
    );
    expect(await screen.findByText("release-bot")).toBeInTheDocument();
    expect(mockListServiceAccounts).toHaveBeenLastCalledWith({
      query: { pageSize: 200, pageToken: "accounts-page-2" },
      signal: expect.any(AbortSignal),
    });

    await user.click(
      await screen.findByRole("button", { name: "Load more credentials" }),
    );
    expect(await screen.findByText("jenkins-green")).toBeInTheDocument();
    expect(mockListServiceAccountCredentials).toHaveBeenLastCalledWith({
      path: { serviceAccountId: accountId },
      query: { pageSize: 200, pageToken: "credentials-page-2" },
      signal: expect.any(AbortSignal),
    });
  });

  it("does not replace the selected account with a slower previous credential response", async () => {
    const user = userEvent.setup();
    let resolveFirst!: (value: unknown) => void;
    const firstResponse = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    const secondCredential = {
      ...newCredential,
      serviceAccountId: secondAccount.id,
      name: "release-green",
    };
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [activeAccount, secondAccount] },
    } as never);
    mockListServiceAccountCredentials
      .mockReturnValueOnce(firstResponse as never)
      .mockResolvedValueOnce({
        data: { items: [secondCredential] },
      } as never);

    renderPage();

    await user.click(await screen.findByText("release-bot"));
    expect(await screen.findByText("release-green")).toBeInTheDocument();

    await act(async () => {
      resolveFirst({ data: { items: [oldCredential] } });
      await firstResponse;
    });
    expect(screen.queryByText("jenkins-blue")).not.toBeInTheDocument();
    expect(screen.getByText("release-green")).toBeInTheDocument();
  });

  it("creates the first stable machine identity from the empty state", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts
      .mockResolvedValueOnce({ data: { items: [] } } as never)
      .mockResolvedValue({ data: { items: [activeAccount] } } as never);
    mockListServiceAccountCredentials.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockCreateServiceAccount.mockResolvedValue({
      data: activeAccount,
    } as never);

    renderPage();

    expect(await screen.findByText("No service accounts")).toBeInTheDocument();
    // The header action is the only primary; the empty state must not twin it.
    const createButtons = screen.getAllByRole("button", {
      name: /New service account/,
    });
    expect(createButtons).toHaveLength(1);
    await user.click(createButtons[0]);
    const dialog = await screen.findByRole("dialog");
    const fields = within(dialog).getAllByRole("textbox");
    await user.type(fields[0], "  pipeone-ci  ");
    await user.type(fields[1], "Publishes PipeOne release artifacts");
    await user.click(within(dialog).getByRole("button", { name: /Create/ }));

    await waitFor(() =>
      expect(mockCreateServiceAccount).toHaveBeenCalledWith({
        body: {
          name: "pipeone-ci",
          description: "Publishes PipeOne release artifacts",
        },
      }),
    );
    expect(
      await screen.findByText(`service-account:${accountId}`),
    ).toBeInTheDocument();
  });

  it("issues, reveals, and revokes credentials without changing the grant principal", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [activeAccount] },
    } as never);
    mockListServiceAccountCredentials
      .mockResolvedValueOnce({ data: { items: [oldCredential] } } as never)
      .mockResolvedValueOnce({
        data: { items: [oldCredential, newCredential] },
      } as never)
      .mockResolvedValue({ data: { items: [newCredential] } } as never);
    mockCreateServiceAccountCredential.mockResolvedValue({
      data: { ...newCredential, token: "agc_once_only_secret" },
    } as never);
    mockRevokeServiceAccountCredential.mockResolvedValue({} as never);

    renderPage();

    await user.click(
      await screen.findByRole("button", { name: /Issue credential/ }),
    );
    const issueDialog = await screen.findByRole("dialog");
    await user.type(within(issueDialog).getByRole("textbox"), "jenkins-green");
    await user.click(
      within(issueDialog).getByRole("button", { name: /Issue/ }),
    );

    expect(await screen.findByText("agc_once_only_secret")).toBeInTheDocument();
    expect(mockCreateServiceAccountCredential).toHaveBeenCalledWith(
      expect.objectContaining({
        path: { serviceAccountId: accountId },
        body: expect.objectContaining({ name: "jenkins-green" }),
      }),
    );
    await user.click(
      screen.getByRole("button", { name: /I stored it securely/ }),
    );
    expect(
      screen.getByText(`service-account:${accountId}`),
    ).toBeInTheDocument();

    await user.click(
      await screen.findByRole("button", { name: "Revoke jenkins-blue" }),
    );
    const revokeDialog = await screen.findByRole("dialog");
    await user.click(
      within(revokeDialog).getByRole("button", { name: /Revoke/ }),
    );

    await waitFor(() =>
      expect(mockRevokeServiceAccountCredential).toHaveBeenCalledWith({
        path: {
          serviceAccountId: accountId,
          credentialId: oldCredentialId,
        },
      }),
    );
    expect(await screen.findByText("jenkins-green")).toBeInTheDocument();
  });

  it("disables and re-enables every credential through the stable account state", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts
      .mockResolvedValueOnce({ data: { items: [activeAccount] } } as never)
      .mockResolvedValueOnce({ data: { items: [disabledAccount] } } as never)
      .mockResolvedValue({ data: { items: [activeAccount] } } as never);
    mockListServiceAccountCredentials.mockResolvedValue({
      data: { items: [oldCredential] },
    } as never);
    mockUpdateServiceAccount.mockResolvedValue({} as never);

    renderPage();

    await user.click(
      await screen.findByRole("button", { name: /Disable account/ }),
    );
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: /Disable/,
      }),
    );
    await waitFor(() =>
      expect(mockUpdateServiceAccount).toHaveBeenNthCalledWith(1, {
        path: { serviceAccountId: accountId },
        headers: { "If-Match": "3" },
        body: { state: "disabled" },
      }),
    );

    const enable = await screen.findByRole("button", {
      name: /Enable account/,
    });
    expect(
      screen.getByRole("button", { name: /Issue credential/ }),
    ).toBeDisabled();
    await user.click(enable);
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: /Enable/,
      }),
    );
    await waitFor(() =>
      expect(mockUpdateServiceAccount).toHaveBeenNthCalledWith(2, {
        path: { serviceAccountId: accountId },
        headers: { "If-Match": "4" },
        body: { state: "active" },
      }),
    );
    expect(
      await screen.findByRole("button", { name: /Disable account/ }),
    ).toBeInTheDocument();
  });

  it("holds the create dialog shut while saving and frees it after a failure", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockListServiceAccountCredentials.mockResolvedValue({
      data: { items: [] },
    } as never);
    let rejectSave: (error: unknown) => void = () => {};
    mockCreateServiceAccount.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectSave = reject;
        }) as never,
    );

    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /New service account/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getAllByRole("textbox")[0], "pipeone-ci");
    await user.click(within(dialog).getByRole("button", { name: /Create/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rejectSave(new Error("network down"));
    // The failure has to be readable while the dialog stays open.
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("holds the credential dialog shut while the issuance is in flight", async () => {
    const user = userEvent.setup();
    mockListServiceAccounts.mockResolvedValue({
      data: { items: [activeAccount] },
    } as never);
    mockListServiceAccountCredentials.mockResolvedValue({
      data: { items: [oldCredential] },
    } as never);
    let rejectSave: (error: unknown) => void = () => {};
    mockCreateServiceAccountCredential.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectSave = reject;
        }) as never,
    );

    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /Issue credential/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox"), "jenkins-green");
    await user.click(within(dialog).getByRole("button", { name: /Issue/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rejectSave(new Error("network down"));
    // The failure has to be readable while the dialog stays open.
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });
});
