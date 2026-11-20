import { MemoryRouter } from "react-router-dom";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  deleteServiceAccountAssertionAudience,
  listServiceAccountAssertionAudiences,
  putServiceAccountAssertionAudience,
  type ServiceAccount,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AssertionAudiences } from "./AssertionAudiences";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    deleteServiceAccountAssertionAudience: vi.fn(),
    listServiceAccountAssertionAudiences: vi.fn(),
    putServiceAccountAssertionAudience: vi.fn(),
  };
});

const accountId = "11111111-1111-4111-8111-111111111111";
const account = {
  id: accountId,
  name: "pipeone-ci",
  description: "",
  state: "active" as const,
  createdAt: "2026-08-18T00:00:00Z",
  updatedAt: "2026-08-18T00:00:00Z",
  version: "3",
  assertionSubject: "notarx:service-account:pipeone-ci",
};
const entra = {
  serviceAccountId: accountId,
  audience: "api://AzureADTokenExchange",
  maxLifetimeSeconds: 300,
  createdAt: "2026-08-18T00:00:00Z",
};

const mockList = vi.mocked(listServiceAccountAssertionAudiences);
const mockPut = vi.mocked(putServiceAccountAssertionAudience);
const mockDelete = vi.mocked(deleteServiceAccountAssertionAudience);

function renderCard(subject: ServiceAccount = account) {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <AssertionAudiences account={subject} />
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

describe("assertion audiences", () => {
  it("shows the fixed subject and the allow-list", async () => {
    mockList.mockResolvedValue({ data: { items: [entra] } } as never);
    renderCard();
    expect(await screen.findByText(entra.audience)).toBeVisible();
    expect(screen.getByText("notarx:service-account:pipeone-ci")).toBeVisible();
    expect(screen.getByText("300 s")).toBeVisible();
    expect(mockList).toHaveBeenCalledWith(
      expect.objectContaining({ path: { serviceAccountId: accountId } }),
    );
  });

  it("says that nothing is allowed until an audience is added", async () => {
    mockList.mockResolvedValue({ data: { items: [] } } as never);
    renderCard();
    expect(await screen.findByText("No audiences allowed")).toBeVisible();
  });

  it("refuses to enable an account without a subject", async () => {
    mockList.mockResolvedValue({ data: { items: [] } } as never);
    renderCard({ ...account, assertionSubject: undefined });
    expect(
      await screen.findByText("This account has no assertion subject"),
    ).toBeVisible();
    expect(
      screen.getByRole("button", { name: /Allow audience/ }),
    ).toBeDisabled();
  });

  it("disables adding for a disabled account", async () => {
    mockList.mockResolvedValue({ data: { items: [] } } as never);
    renderCard({ ...account, state: "disabled" });
    await screen.findByText("No audiences allowed");
    expect(
      screen.getByRole("button", { name: /Allow audience/ }),
    ).toBeDisabled();
  });

  it("adds an audience with the chosen lifetime and reads the list again", async () => {
    mockList
      .mockResolvedValueOnce({ data: { items: [] } } as never)
      .mockResolvedValueOnce({ data: { items: [entra] } } as never);
    mockPut.mockResolvedValue({ data: entra } as never);
    renderCard();
    await screen.findByText("No audiences allowed");
    await userEvent.click(
      screen.getByRole("button", { name: /Allow audience/ }),
    );
    const dialog = await screen.findByRole("dialog");
    const allow = Array.from(dialog.querySelectorAll("button")).find(
      (button) => button.textContent === "Allow",
    ) as HTMLButtonElement;
    expect(allow).toBeDisabled();
    await userEvent.type(
      dialog.querySelector("input") as HTMLInputElement,
      "  api://AzureADTokenExchange ",
    );
    await userEvent.click(allow);
    await waitFor(() =>
      expect(mockPut).toHaveBeenCalledWith({
        path: { serviceAccountId: accountId },
        body: {
          audience: "api://AzureADTokenExchange",
          maxLifetimeSeconds: 600,
        },
      }),
    );
    expect(await screen.findByText(entra.audience)).toBeVisible();
    expect(mockList).toHaveBeenCalledTimes(2);
  });

  it("keeps a failed save in the dialog", async () => {
    mockList.mockResolvedValue({ data: { items: [] } } as never);
    mockPut.mockResolvedValue({
      error: { code: "invalid_request", message: "audience rejected" },
    } as never);
    renderCard();
    await screen.findByText("No audiences allowed");
    await userEvent.click(
      screen.getByRole("button", { name: /Allow audience/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(
      dialog.querySelector("input") as HTMLInputElement,
      "x",
    );
    await userEvent.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Allow",
      ) as HTMLButtonElement,
    );
    expect(await screen.findByText(/audience rejected/)).toBeVisible();
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("removes an audience after a confirmation", async () => {
    mockList
      .mockResolvedValueOnce({ data: { items: [entra] } } as never)
      .mockResolvedValueOnce({ data: { items: [] } } as never);
    mockDelete.mockResolvedValue({ data: undefined } as never);
    renderCard();
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Remove audience api://AzureADTokenExchange",
      }),
    );
    expect(mockDelete).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Remove",
      ) as HTMLButtonElement,
    );
    await waitFor(() =>
      expect(mockDelete).toHaveBeenCalledWith({
        path: { serviceAccountId: accountId },
        query: { audience: entra.audience },
      }),
    );
    expect(await screen.findByText("No audiences allowed")).toBeVisible();
  });

  it("keeps the list and shows the error when a removal fails", async () => {
    mockList.mockResolvedValue({ data: { items: [entra] } } as never);
    mockDelete.mockResolvedValue({
      error: { code: "not_found", message: "audience is gone" },
    } as never);
    renderCard();
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Remove audience api://AzureADTokenExchange",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Remove",
      ) as HTMLButtonElement,
    );
    expect(await screen.findByText(/audience is gone/)).toBeVisible();
    expect(screen.getByText(entra.audience)).toBeVisible();
  });

  it("contains a malformed list", async () => {
    mockList.mockResolvedValue({ data: { items: [null] } } as never);
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      renderCard();
      expect(
        await screen.findByText("Invalid assertion audiences response"),
      ).toBeVisible();
    } finally {
      errors.mockRestore();
    }
  });
});
