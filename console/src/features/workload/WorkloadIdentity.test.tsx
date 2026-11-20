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
  createWorkloadTrustPolicy,
  listServiceAccounts,
  listWorkloadIssuers,
  listWorkloadTrustPolicies,
  probeWorkloadIssuer,
  testWorkloadTrustPolicy,
  updateWorkloadIssuer,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { WorkloadIdentityPage } from "./WorkloadIdentity";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    createWorkloadIssuer: vi.fn(),
    createWorkloadTrustPolicy: vi.fn(),
    deleteWorkloadIssuer: vi.fn(),
    deleteWorkloadTrustPolicy: vi.fn(),
    listServiceAccounts: vi.fn(),
    listWorkloadIssuers: vi.fn(),
    listWorkloadTrustPolicies: vi.fn(),
    probeWorkloadIssuer: vi.fn(),
    testWorkloadTrustPolicy: vi.fn(),
    updateWorkloadIssuer: vi.fn(),
    updateWorkloadTrustPolicy: vi.fn(),
  };
});

const stamp = "2026-11-01T00:00:00Z";
const kinds = ["github_actions", "gitlab_ci", "kubernetes", "generic"] as const;
const issuers = kinds.map((kind, index) => ({
  id: `00000000-0000-4000-8000-00000000000${index + 1}`,
  name: `issuer-${kind.replace("_", "-")}`,
  kind,
  issuer: `https://${kind}.example.test`,
  audiences: ["https://notarx.example.test"],
  allowedAlgs: ["RS256" as const],
  jwksMode: "discovery" as const,
  allowPrivateNetwork: false,
  state: "active" as const,
  version: "1",
  createdAt: stamp,
  updatedAt: stamp,
}));
const accountId = "11111111-1111-4111-8111-111111111111";
const policy = {
  id: "22222222-2222-4222-8222-222222222222",
  name: "publish",
  issuerId: issuers[0].id,
  serviceAccountId: accountId,
  conditions: [{ claim: ["repository_id"], op: "eq" as const, values: ["1"] }],
  maxTokenTtlSeconds: 900,
  singleUse: false,
  state: "active" as const,
  version: "1",
  createdAt: stamp,
  updatedAt: stamp,
};

function mockLists() {
  vi.mocked(listWorkloadIssuers).mockResolvedValue({
    data: { items: issuers },
  } as never);
  vi.mocked(listWorkloadTrustPolicies).mockResolvedValue({
    data: { items: [policy] },
  } as never);
  vi.mocked(listServiceAccounts).mockResolvedValue({
    data: {
      items: [
        {
          id: accountId,
          name: "feeds-publisher",
          description: "",
          state: "active",
          createdAt: stamp,
          updatedAt: stamp,
          version: "1",
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
            <WorkloadIdentityPage />
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

async function choose(index: number, option: string) {
  const dialog = screen.getByRole("dialog");
  await userEvent.click(within(dialog).getAllByRole("combobox")[index]);
  await userEvent.click(await screen.findByTitle(option));
}

describe("Workload identity page", () => {
  it("lists issuers of all four kinds and the policies", async () => {
    mockLists();
    renderPage();
    for (const issuer of issuers) {
      expect((await screen.findAllByText(issuer.name))[0]).toBeVisible();
    }
    for (const label of [
      "GitHub Actions",
      "GitLab CI",
      "Kubernetes",
      "Generic OIDC",
    ]) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    expect(await screen.findByText("publish")).toBeVisible();
    expect(screen.getByText("feeds-publisher")).toBeVisible();
    expect(screen.getByText("1 conditions")).toBeVisible();
  });

  it.each(issuers)(
    "fills the $kind template and submits the policy",
    async (issuer) => {
      mockLists();
      vi.mocked(createWorkloadTrustPolicy).mockResolvedValue({
        data: { ...policy, name: "new-policy" },
      } as never);
      renderPage();
      await screen.findAllByText(issuer.name);
      await userEvent.click(screen.getByRole("button", { name: /New policy/ }));
      const dialog = screen.getByRole("dialog");
      await userEvent.type(
        within(dialog).getByLabelText("Policy name"),
        "new-policy",
      );
      await choose(0, issuer.name);
      await choose(1, "feeds-publisher");
      const editor = within(dialog).getByLabelText(
        /Conditions/,
      ) as HTMLTextAreaElement;
      // The template holds the mandatory condition of the kind.
      expect(editor.value).toContain(
        issuer.kind === "github_actions"
          ? "repository_id"
          : issuer.kind === "gitlab_ci"
            ? "project_id"
            : "sub",
      );
      await userEvent.click(
        within(dialog).getByRole("button", { name: "Create" }),
      );
      await waitFor(() =>
        expect(createWorkloadTrustPolicy).toHaveBeenCalledTimes(1),
      );
      const call = vi.mocked(createWorkloadTrustPolicy).mock.calls[0][0].body;
      expect(call).toMatchObject({
        name: "new-policy",
        issuerId: issuer.id,
        serviceAccountId: accountId,
        maxTokenTtlSeconds: 900,
        singleUse: false,
      });
      expect(call.conditions.length).toBeGreaterThan(0);
    },
  );

  it("turns single use on for an exchange-only policy", async () => {
    mockLists();
    vi.mocked(createWorkloadTrustPolicy).mockResolvedValue({
      data: policy,
    } as never);
    renderPage();
    await screen.findAllByText(issuers[0].name);
    await userEvent.click(screen.getByRole("button", { name: /New policy/ }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(
      within(dialog).getByLabelText("Policy name"),
      "exchange",
    );
    await choose(0, issuers[0].name);
    await choose(1, "feeds-publisher");
    await userEvent.click(
      within(dialog).getByRole("switch", {
        name: "Used only through the token exchange",
      }),
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create" }),
    );
    await waitFor(() => expect(createWorkloadTrustPolicy).toHaveBeenCalled());
    expect(
      vi.mocked(createWorkloadTrustPolicy).mock.calls[0][0].body.singleUse,
    ).toBe(true);
  });

  it("shows the dry-run result with the failing condition and the jti warning", async () => {
    mockLists();
    vi.mocked(testWorkloadTrustPolicy).mockResolvedValue({
      data: {
        signatureVerified: false,
        verificationError: "token is expired",
        results: [
          {
            policyId: policy.id,
            policyName: "publish",
            matched: false,
            failure: {
              conditionIndex: 0,
              claim: ["repository_id"],
              reason: "claim value does not match",
            },
          },
        ],
        warnings: ["the token has no jti"],
      },
    } as never);
    renderPage();
    await screen.findByText("publish");
    await userEvent.type(
      screen.getByLabelText(/Token or claims/),
      '{{"iss":"x","sub":"y"}',
    );
    await userEvent.click(screen.getByRole("button", { name: /Run the test/ }));
    const result = await screen.findByTestId("workload-test-result");
    expect(within(result).getByText("No policy matches.")).toBeVisible();
    expect(
      within(result).getByText(/publish: condition 0 on repository_id failed/),
    ).toBeVisible();
    expect(within(result).getByText(/the token has no jti/)).toBeVisible();
    expect(testWorkloadTrustPolicy).toHaveBeenCalledWith({
      body: { claims: { iss: "x", sub: "y" } },
    });
  });

  it("probes an issuer and re-enables a disabled one", async () => {
    mockLists();
    vi.mocked(listWorkloadIssuers).mockResolvedValue({
      data: { items: [{ ...issuers[0], state: "disabled" }] },
    } as never);
    vi.mocked(probeWorkloadIssuer).mockResolvedValue({
      data: {
        reachable: true,
        keys: [{ keyId: "k1", keyType: "RSA" }],
        algorithms: ["RS256"],
      },
    } as never);
    vi.mocked(updateWorkloadIssuer).mockResolvedValue({
      data: issuers[0],
    } as never);
    renderPage();
    await screen.findAllByText(issuers[0].name);
    await userEvent.click(
      screen.getByRole("button", { name: "Check reachability" }),
    );
    expect(
      await screen.findByText("The issuer answered and published keys."),
    ).toBeInTheDocument();
    expect(screen.getByText(/k1 \(RSA\)/)).toBeInTheDocument();
    await userEvent.click(
      within(screen.getByRole("dialog")).getAllByRole("button", {
        name: "Close",
      })[0],
    );
    const row = screen
      .getAllByText(issuers[0].name)[0]
      .closest("tr") as HTMLElement;
    await userEvent.click(within(row).getByRole("button", { name: "Enable" }));
    await waitFor(() =>
      expect(updateWorkloadIssuer).toHaveBeenCalledWith(
        expect.objectContaining({
          headers: { "If-Match": "1" },
          body: { state: "active" },
        }),
      ),
    );
  });
});
