import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { PreferencesProvider } from "../../lib/preferences";
import { SearchOutlined } from "@ant-design/icons";
import { EmptyState, ErrorBanner, Loading, Notice } from "./Feedback";

afterEach(cleanup);

describe("Loading", () => {
  it("announces the busy state without requiring visual inspection", () => {
    render(
      <PreferencesProvider>
        <Loading label="Verifying artifact digest…" />
      </PreferencesProvider>,
    );

    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-live", "polite");
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(status).toHaveTextContent("Verifying artifact digest…");
  });
});

describe("ErrorBanner", () => {
  it("preserves plain-text server errors instead of blaming the network or token", () => {
    render(
      <PreferencesProvider>
        <ErrorBanner error="upstream maintenance in progress" />
      </PreferencesProvider>,
    );

    expect(screen.getByText("upstream maintenance in progress")).toBeVisible();
    expect(
      screen.queryByText("Request failed. Check the network or token."),
    ).not.toBeInTheDocument();
  });

  it("turns an unmounted API route into an actionable version-mismatch message", () => {
    render(
      <PreferencesProvider>
        <ErrorBanner error="404 page not found" />
      </PreferencesProvider>,
    );

    expect(
      screen.getByText(
        "The connected Gateway does not expose this endpoint. The Console and Gateway versions may not match; update or restart Gateway and retry.",
      ),
    ).toBeVisible();
  });
});

describe("EmptyState", () => {
  it("renders a quiet semantic icon without decorative imagery", () => {
    render(
      <PreferencesProvider>
        <EmptyState
          title="No repositories"
          hint="Create the first repository"
          action={<button type="button">New repository</button>}
        />
      </PreferencesProvider>,
    );

    expect(document.querySelector("img")).not.toBeInTheDocument();
    expect(document.querySelector(".ag-empty-state-icon")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
    expect(screen.getByText("No repositories")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "New repository" }),
    ).toBeVisible();
  });

  it("supports compact contextual states without changing their semantics", () => {
    render(
      <PreferencesProvider>
        <EmptyState
          compact
          className="catalog-empty"
          icon={<SearchOutlined data-testid="search-empty-icon" />}
          title="No public repositories"
          hint="Public sources will appear here."
          action={<button type="button">Open management</button>}
        />
      </PreferencesProvider>,
    );

    const empty = screen
      .getByText("No public repositories")
      .closest(".ant-empty");
    expect(empty).toHaveClass(
      "ag-empty-state",
      "ag-empty-state-compact",
      "catalog-empty",
    );
    expect(screen.getByTestId("search-empty-icon")).toBeInTheDocument();
    expect(screen.getByText("Public sources will appear here.")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Open management" }),
    ).toBeVisible();
  });
});

describe("Notice", () => {
  it("is closable by default and reports the close to the caller", async () => {
    const onClose = vi.fn();
    render(
      <PreferencesProvider>
        <Notice tone="success" title="Saved" onClose={onClose} />
      </PreferencesProvider>,
    );

    expect(screen.getByText("Saved")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps standing explanations open and without an icon", () => {
    const { container } = render(
      <PreferencesProvider>
        <Notice tone="info" title="This is a note" />
      </PreferencesProvider>,
    );

    expect(screen.getByText("This is a note")).toBeVisible();
    expect(container.querySelector(".ant-alert-icon")).toBeNull();
    expect(screen.queryByRole("button", { name: /close/i })).toBeNull();
  });

  it("renders the warning tone for failures the page can live with", () => {
    const { container } = render(
      <PreferencesProvider>
        <ErrorBanner error={new Error("upstream slow")} tone="warning" />
      </PreferencesProvider>,
    );
    expect(container.querySelector(".ant-alert-warning")).not.toBeNull();
  });
});
