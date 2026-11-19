import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { afterEach, expect, it, vi } from "vitest";
import {
  listIdentityProviders,
  replaceIdentityProvider,
  type IdentityProvider,
} from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { ConsoleQueryProvider } from "../../lib/query/QueryProvider";
import { AuthenticationPage } from "./Authentication";

const auth = vi.hoisted(() => ({
  token: "admin-token",
  identity: { actor: "user:admin" } as { actor: string } | null,
  handleAuthFailure: vi.fn(),
}));
vi.mock("../../lib/auth", () => ({ useAuth: () => auth }));
vi.mock("../../client", async (original) => ({
  ...(await original<typeof import("../../client")>()),
  listIdentityProviders: vi.fn(),
  replaceIdentityProvider: vi.fn(),
  testIdentityProvider: vi.fn(),
}));
const saved: IdentityProvider = {
  id: "00000000-0000-0000-0000-000000000301",
  slug: "default",
  kind: "oidc",
  displayName: "Single sign-on",
  enabled: true,
  version: "4",
  attributeMapping: {},
  provisioningMode: "jit",
  emailLinkingEnabled: false,
  jitDefaultRole: "none",
  createdAt: "2026-10-01T08:00:00Z",
  updatedAt: "2026-10-12T08:00:00Z",
  oidc: {
    issuer: "https://id.example.com/realms/artifacts",
    audience: "gateway-api",
    clientId: "console",
    clientSecretConfigured: true,
    redirectUrl: "https://gateway.example.com/auth/oidc/default/callback",
    scopes: ["openid"],
    adminSubjects: [],
    memberRoles: [],
    adminRoles: [],
    algorithms: ["RS256"],
  },
};
const listOf = (...items: IdentityProvider[]) => ({ data: { items } }) as never;
let queryClient: ReturnType<typeof useQueryClient>;
const oidcKey = ["settings", "identity-providers"];
function Capture() {
  const client = useQueryClient();
  useEffect(() => {
    queryClient = client;
  }, [client]);
  return null;
}
function Harness({ show = true }: { show?: boolean }) {
  return (
    <ConsoleQueryProvider>
      <Capture />
      <PreferencesProvider>
        {show && <AuthenticationPage />}
      </PreferencesProvider>
    </ConsoleQueryProvider>
  );
}
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  localStorage.clear();
  auth.token = "admin-token";
  auth.identity = { actor: "user:admin" };
});

it.each([false, true])(
  "ignores a late OIDC save after sign-out (new identity=%s)",
  async (newIdentity) => {
    const user = userEvent.setup();
    let finish!: (result: unknown) => void;
    vi.mocked(listIdentityProviders).mockResolvedValue(listOf(saved));
    vi.mocked(replaceIdentityProvider).mockImplementation(
      () =>
        new Promise<never>((resolve) => {
          finish = (result) => resolve(result as never);
        }),
    );
    const { rerender } = render(<Harness />);
    const audience = await screen.findByDisplayValue("gateway-api");
    await user.clear(audience);
    await user.type(audience, "admin-private-audience");
    await user.click(screen.getByRole("button", { name: /Save and apply/ }));
    await waitFor(() =>
      expect(replaceIdentityProvider).toHaveBeenCalledTimes(1),
    );
    auth.token = "";
    auth.identity = null;
    rerender(<Harness show={false} />);
    await waitFor(() =>
      expect(queryClient.getQueryData(oidcKey)).toBeUndefined(),
    );
    const nextSettings = {
      ...saved,
      version: "6",
      oidc: { ...saved.oidc!, audience: "bob-audience" },
    };
    if (newIdentity) {
      auth.token = "bob-token";
      auth.identity = { actor: "user:bob" };
      vi.mocked(listIdentityProviders).mockResolvedValue(listOf(nextSettings));
      rerender(<Harness />);
      await screen.findByDisplayValue("bob-audience");
    }
    await act(async () =>
      finish({
        data: {
          ...saved,
          version: "5",
          oidc: { ...saved.oidc!, audience: "admin-private-audience" },
        },
      }),
    );
    expect(queryClient.getQueryData(oidcKey)).toEqual(
      newIdentity ? nextSettings : undefined,
    );
  },
);

it("retains an OIDC draft after a failed background 5xx read", async () => {
  const user = userEvent.setup();
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf(saved));
  render(<Harness />);
  const audience = await screen.findByDisplayValue("gateway-api");
  await user.clear(audience);
  await user.type(audience, "unsaved-audience");
  vi.mocked(listIdentityProviders).mockResolvedValue({
    error: { status: 503, message: "read unavailable" },
  } as never);
  await act(async () => {
    await queryClient.refetchQueries({ queryKey: oidcKey });
  });
  await screen.findByText("read unavailable");
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf(saved));
  await user.click(screen.getByRole("button", { name: /Retry/ }));
  await waitFor(() =>
    expect(queryClient.getQueryState(oidcKey)?.error).toBeNull(),
  );
  expect(screen.getByDisplayValue("unsaved-audience")).toBeInTheDocument();
});
