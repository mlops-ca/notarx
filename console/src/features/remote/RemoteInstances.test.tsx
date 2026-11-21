import { MemoryRouter } from "react-router-dom";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createRemoteInstance,
  deleteRemoteInstance,
  listRemoteInstances,
  testRemoteInstance,
  updateRemoteInstance,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { RemoteInstancesPage } from "./RemoteInstances";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    createRemoteInstance: vi.fn(),
    deleteRemoteInstance: vi.fn(),
    listRemoteInstances: vi.fn(),
    testRemoteInstance: vi.fn(),
    updateRemoteInstance: vi.fn(),
  };
});

const stamp = "2026-11-01T00:00:00Z";
const staging = {
  id: "00000000-0000-4000-8000-000000000001",
  name: "staging",
  baseUrl: "https://staging.example.test",
  instanceId: "01990000-0000-7000-8000-000000000001",
  authKind: "static_token" as const,
  audience: "",
  tokenConfigured: true,
  trustEvidence: false,
  state: "active" as const,
  version: "4",
  createdAt: stamp,
  updatedAt: stamp,
};

const mockList = vi.mocked(listRemoteInstances);
const mockCreate = vi.mocked(createRemoteInstance);
const mockUpdate = vi.mocked(updateRemoteInstance);
const mockDelete = vi.mocked(deleteRemoteInstance);
const mockTest = vi.mocked(testRemoteInstance);

function renderPage() {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <RemoteInstancesPage />
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

describe("remote instances page", () => {
  it("lists instances and shows only that a token is configured", async () => {
    mockList.mockResolvedValue({ data: { items: [staging] } } as never);
    renderPage();
    expect(await screen.findByText("staging")).toBeVisible();
    expect(screen.getByText("Token configured")).toBeVisible();
    expect(screen.queryByDisplayValue(/./)).toBeNull();
  });

  it("adds an instance with a write-only password token", async () => {
    const user = userEvent.setup();
    mockList.mockResolvedValue({ data: { items: [] } } as never);
    mockCreate.mockResolvedValue({ data: staging } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: /Add remote instance/ }),
    );
    await user.type(await screen.findByLabelText("Name"), "staging");
    await user.type(
      screen.getByLabelText("Base URL"),
      "https://staging.example.test",
    );
    await user.click(screen.getByLabelText("Static token"));
    const token = screen.getByLabelText("Token", { selector: "input" });
    expect(token).toHaveAttribute("type", "password");
    expect(token).toHaveValue("");
    await user.type(token, "s3cret");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(mockCreate).toHaveBeenCalled());
    expect(mockCreate.mock.calls[0]?.[0]).toEqual({
      body: {
        name: "staging",
        baseUrl: "https://staging.example.test",
        authKind: "static_token",
        trustEvidence: false,
        token: "s3cret",
      },
    });
  });

  it("keeps the sealed token when an edit leaves the field empty", async () => {
    const user = userEvent.setup();
    mockList.mockResolvedValue({ data: { items: [staging] } } as never);
    mockUpdate.mockResolvedValue({ data: staging } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: "Edit staging" }),
    );
    const token = await screen.findByLabelText("Token", { selector: "input" });
    expect(token).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalled());
    const call = mockUpdate.mock.calls[0]?.[0];
    expect(call?.headers).toEqual({ "If-Match": "4" });
    expect(call?.body).not.toHaveProperty("token");
  });

  it("tests an instance and reports the outcome", async () => {
    const user = userEvent.setup();
    mockList.mockResolvedValue({ data: { items: [staging] } } as never);
    mockTest.mockResolvedValue({
      data: {
        reachable: false,
        state: "unreachable",
        checkedAt: stamp,
        formats: [],
        error: "connection refused",
      },
    } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: "Test staging" }),
    );
    expect(await screen.findByText("staging is unreachable")).toBeVisible();
    expect(screen.getByText("Reason: connection refused")).toBeVisible();
    expect(mockTest).toHaveBeenCalledWith({
      path: { remoteInstanceId: staging.id },
    });
  });

  it("names the promotion rules that block deleting an instance", async () => {
    const user = userEvent.setup();
    mockList.mockResolvedValue({ data: { items: [staging] } } as never);
    mockDelete.mockResolvedValue({
      error: {
        status: 409,
        code: "in_use",
        message: "remote instance is still the target of a promotion rule",
        rules: [
          { ruleId: "7c0f0a52-6d5b-4a43-8d2d-1a1c4a9b0e11", name: "nightly" },
          { ruleId: "9e3d6f7e-4f2e-4a4a-8e60-5b0b2d3c9f22", name: "release" },
        ],
      },
    } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: "Delete staging" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Delete",
      )!,
    );
    expect(
      await screen.findByText(
        /promotion rules still target it \(nightly, release\)/,
      ),
    ).toBeInTheDocument();
  });

  it("deletes an instance after confirmation", async () => {
    const user = userEvent.setup();
    mockList.mockResolvedValue({ data: { items: [staging] } } as never);
    mockDelete.mockResolvedValue({ data: undefined } as never);
    renderPage();
    await user.click(
      await screen.findByRole("button", { name: "Delete staging" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Delete staging?");
    await user.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Delete",
      )!,
    );
    await waitFor(() =>
      expect(mockDelete).toHaveBeenCalledWith({
        path: { remoteInstanceId: staging.id },
      }),
    );
  });
});
