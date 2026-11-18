import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AuthorizationRole } from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { GrantForm } from "./GrantForm";
import type { DraftGrant, PrincipalOption } from "./grantDraft";

vi.mock("../../client", () => ({
  listApiKeys: vi.fn(),
  listAuthorizationRoles: vi.fn(),
  listGrants: vi.fn(),
  listServiceAccounts: vi.fn(),
  listUsers: vi.fn(),
  replaceGrants: vi.fn(),
}));

const principalChoices: PrincipalOption[] = [
  { value: "user:alice", label: "User · alice", detail: "Global role member" },
  {
    value: "api-key:key-1",
    label: "API Key · deploy",
    detail: "Global role admin",
  },
  {
    value: "service-account:sa-1",
    label: "Service account · release-bot",
    detail: "No global role; repository grants decide",
  },
];

const releaseRole: AuthorizationRole = {
  id: "role-release",
  name: "Release publisher",
  scopes: ["repositories:read", "repositories:write"],
  version: "1",
  createdAt: "2026-08-18T00:00:00Z",
  updatedAt: "2026-08-18T00:00:00Z",
};

function draft(overrides: Partial<DraftGrant> = {}): DraftGrant {
  return {
    principal: "user:alice",
    scopes: ["repositories:read"],
    ...overrides,
  };
}

function renderForm(
  grant: DraftGrant,
  roles: AuthorizationRole[] = [],
  options: { lockPrincipal?: boolean } = {},
) {
  const onChange = vi.fn<(next: DraftGrant) => void>();
  render(
    <PreferencesProvider>
      <AntdProvider>
        <GrantForm
          grant={grant}
          principalOptions={principalChoices}
          authorizationRoles={roles}
          format="raw"
          onChange={onChange}
          {...options}
        />
      </AntdProvider>
    </PreferencesProvider>,
  );
  return { onChange };
}

function lastChange(onChange: ReturnType<typeof renderForm>["onChange"]) {
  return onChange.mock.calls.at(-1)?.[0];
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("GrantForm", () => {
  it("labels every field where the administrator reads it", () => {
    renderForm(draft());

    expect(screen.getByText("Principal")).toBeInTheDocument();
    expect(screen.getByText("Permission")).toBeInTheDocument();
    expect(screen.getByText("Resource scope")).toBeInTheDocument();
    expect(screen.getByText("Granted by this rule")).toBeInTheDocument();
    // Each visible label is also the control's accessible name.
    expect(
      screen.getByRole("combobox", { name: "Principal" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "Permission" }),
    ).toBeInTheDocument();
  });

  it("offers the principal options it is given", async () => {
    const user = userEvent.setup();
    renderForm(draft({ principal: "" }));

    await user.click(screen.getByRole("combobox", { name: "Principal" }));

    expect(screen.getByText(/User · alice/)).toBeInTheDocument();
    expect(screen.getByText(/API Key · deploy/)).toBeInTheDocument();
    expect(
      screen.getByText(/Service account · release-bot/),
    ).toBeInTheDocument();
    expect(screen.getByText(/OIDC \/ custom actor/)).toBeInTheDocument();
  });

  it("names principal options for screen readers by their readable label", async () => {
    const user = userEvent.setup();
    renderForm(draft({ principal: "" }));
    // Searching leaves the service account as the announced option.
    await user.type(
      screen.getByRole("combobox", { name: "Principal" }),
      "release",
    );
    expect(
      screen.getByRole("option", { name: /^Service account · release-bot/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: /service-account:/ }),
    ).toBeNull();
  });

  it("shows a fixed principal as text instead of a select that cannot change", () => {
    renderForm(draft(), [], { lockPrincipal: true });

    expect(
      screen.queryByRole("combobox", { name: "Principal" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("user:alice")).toBeInTheDocument();
  });

  it("copies the scopes of a chosen custom authorization role", async () => {
    const user = userEvent.setup();
    const { onChange } = renderForm(draft(), [releaseRole]);

    await user.click(screen.getByRole("combobox", { name: "Permission" }));
    await user.click(screen.getByText("Release publisher"));

    const next = lastChange(onChange);
    expect(next?.scopes).toEqual(["repositories:read", "repositories:write"]);
    expect(next?.scopes).not.toBe(releaseRole.scopes);
    expect(next?.roleId).toBe("role-release");
    expect(next?.principal).toBe("user:alice");
  });

  it("maps a chosen built-in level to that level's scopes", async () => {
    const user = userEvent.setup();
    const { onChange } = renderForm(draft({ scopes: ["repositories:read"] }), [
      releaseRole,
    ]);

    await user.click(screen.getByRole("combobox", { name: "Permission" }));
    await user.click(screen.getByText("Write · publish / edit"));

    const next = lastChange(onChange);
    expect(next?.scopes).toEqual(["repositories:write"]);
    expect(next?.roleId).toBeUndefined();
    expect(next?.principal).toBe("user:alice");
  });

  it("reports a resource prefix change picked from the scope segmented control", async () => {
    const user = userEvent.setup();
    const { onChange } = renderForm(draft({ resourcePrefix: "" }));

    await user.click(screen.getByText("Limit by prefix"));

    expect(lastChange(onChange)?.resourcePrefix).toBe("releases/");
  });

  it("reports a typed resource prefix", async () => {
    const user = userEvent.setup();
    const { onChange } = renderForm(draft({ resourcePrefix: "releases/" }));

    // Principal select, permission select, then the resource prefix input.
    const prefixInput = screen.getAllByRole("combobox")[2];
    await user.type(prefixInput, "{End}x");

    expect(lastChange(onChange)?.resourcePrefix).toBe("releases/x");
  });
});
