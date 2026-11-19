import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { getSecuritySettings } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AuthorizationCacheCard } from "./AuthorizationCache";

vi.mock("../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../client")>()),
  getSecuritySettings: vi.fn(),
}));

function renderCard() {
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AuthorizationCacheCard />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("shows the revocation window in seconds", async () => {
  vi.mocked(getSecuritySettings).mockResolvedValue({
    data: { authorizationCacheEnabled: true, authorizationCacheTtlSeconds: 5 },
  } as never);
  renderCard();
  expect(await screen.findByText("5 s")).toBeTruthy();
  expect(screen.getByText(/NOTARX_AUTHZ_CACHE_TTL=1s/)).toBeTruthy();
});

it("states that a disabled cache is effective immediately", async () => {
  vi.mocked(getSecuritySettings).mockResolvedValue({
    data: { authorizationCacheEnabled: false, authorizationCacheTtlSeconds: 0 },
  } as never);
  renderCard();
  expect(
    await screen.findByText("Caching disabled: effective immediately"),
  ).toBeTruthy();
});

it("renders nothing until the settings load", () => {
  vi.mocked(getSecuritySettings).mockReturnValue(
    new Promise(() => {}) as never,
  );
  renderCard();
  expect(screen.queryByText("Authorization cache")).toBeNull();
});
