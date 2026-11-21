import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { IdentitySummary } from "./IdentitySummary";
import { PreferencesProvider } from "../../lib/preferences";

const renderWithPreferences = (ui: React.ReactNode) =>
  render(<PreferencesProvider>{ui}</PreferencesProvider>);

afterEach(cleanup);

describe("IdentitySummary", () => {
  it("labels a registered unprivileged identity as pending", () => {
    renderWithPreferences(
      <IdentitySummary
        identity={{
          actor: "user:pending",
          kind: "oidc",
          role: "none",
          administrator: false,
        }}
      />,
    );
    expect(screen.getByText("Awaiting approval")).toBeInTheDocument();
  });

  it("flags an administrator subject without exposing unrelated claims", () => {
    renderWithPreferences(
      <IdentitySummary
        identity={{
          actor: "ci-user",
          kind: "oidc",
          role: "member",
          administrator: false,
          oidc: {
            adminSubject: true,
          },
        }}
      />,
    );

    expect(screen.getByText("ci-user")).toBeInTheDocument();
    expect(screen.getByText("OIDC")).toBeInTheDocument();
    expect(screen.getByText("Member")).toBeInTheDocument();
    expect(screen.getByText(/administrator subject/i)).toBeInTheDocument();
  });

  it("makes the absence of a global role explicit", () => {
    renderWithPreferences(
      <IdentitySummary
        identity={{
          actor: "resolver",
          kind: "static_resolver",
          administrator: false,
        }}
      />,
    );

    expect(screen.getByText("Static resolver credential")).toBeInTheDocument();
    expect(
      screen.getByText("None; repository rules decide"),
    ).toBeInTheDocument();
  });
});
