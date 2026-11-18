import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RouteErrorPage } from "./RouteErrorPage";
import { PreferencesProvider } from "../lib/preferences";

const route = vi.hoisted(() => ({
  error: new TypeError(
    "Failed to fetch dynamically imported module: http://localhost:4173/src/app/Layout.tsx",
  ) as unknown,
}));

vi.mock("react-router-dom", async () => {
  const actual =
    await vi.importActual<typeof import("react-router-dom")>(
      "react-router-dom",
    );
  return { ...actual, useRouteError: () => route.error };
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  localStorage.clear();
  route.error = new TypeError(
    "Failed to fetch dynamically imported module: http://localhost:4173/src/app/Layout.tsx",
  );
});

describe("RouteErrorPage", () => {
  it("replaces the developer exception page for a failed lazy module", async () => {
    render(
      <PreferencesProvider>
        <RouteErrorPage />
      </PreferencesProvider>,
    );

    expect(
      await screen.findByRole("heading", {
        name: "Page resources failed to load",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Page resources may have changed, or the development server may be rebuilding dependencies\. Reload the page; if it still fails, restart the local Console\./,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(
      screen.queryByText("Unexpected Application Error!"),
    ).not.toBeInTheDocument();
  });

  it("presents a regular runtime failure with theme-safe recovery actions", async () => {
    localStorage.setItem("ag.console.theme", "light");
    route.error = new TypeError(
      "Cannot read properties of null (reading 'useContext')",
    );

    render(
      <PreferencesProvider>
        <RouteErrorPage />
      </PreferencesProvider>,
    );

    expect(
      await screen.findByRole("heading", { name: "Page failed to load" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Technical details")).toBeInTheDocument();
    expect(
      screen.getByText("Cannot read properties of null (reading 'useContext')"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toHaveClass(
      "ag-route-error-primary",
    );
    expect(
      screen.getByRole("link", { name: "Browse public artifacts" }),
    ).toHaveClass("ag-route-error-secondary");
  });
});
