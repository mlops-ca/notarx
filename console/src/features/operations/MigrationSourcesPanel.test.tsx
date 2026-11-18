import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  deleteMigrationSource,
  discoverMigrationSource,
  getMigrationSource,
  listMigrationSources,
  preflightMigrationSource,
  previewMigrationSourceMapping,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import {
  migrationDiscovery,
  migrationMapping,
  migrationPreflight,
  migrationSource,
} from "../../test/migrationFixtures";
import { MigrationSourcesPanel } from "./MigrationSourcesPanel";

vi.mock("../../client", () => ({
  listMigrationSources: vi.fn(),
  getMigrationSource: vi.fn(),
  createMigrationSource: vi.fn(),
  updateMigrationSource: vi.fn(),
  deleteMigrationSource: vi.fn(),
  preflightMigrationSource: vi.fn(),
  discoverMigrationSource: vi.fn(),
  previewMigrationSourceMapping: vi.fn(),
}));
const list = vi.mocked(listMigrationSources);
const preflight = vi.mocked(preflightMigrationSource);
const discover = vi.mocked(discoverMigrationSource);
const preview = vi.mocked(previewMigrationSourceMapping);
const detail = vi.mocked(getMigrationSource);

function show(source = migrationSource.id) {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <MemoryRouter
            initialEntries={[
              `/operations?tab=migrations${source ? `&source=${source}` : ""}`,
            ]}
          >
            <MigrationSourcesPanel />
          </MemoryRouter>
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}
beforeEach(() => {
  list.mockResolvedValue({
    data: { items: [migrationSource], nextCursor: "" },
  } as never);
  detail.mockResolvedValue({ data: migrationSource } as never);
  preflight.mockResolvedValue({ data: migrationPreflight } as never);
  discover.mockResolvedValue({ data: migrationDiscovery } as never);
  preview.mockResolvedValue({ data: migrationMapping } as never);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe("migration source observations", () => {
  it("renders one initial error and sanitizes transport messages", async () => {
    list.mockRejectedValue(new Error("password=never-render-this"));
    show("");
    await screen.findByText(/request did not complete/);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(
      screen.queryByText("No migration sources configured"),
    ).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("never-render-this");
  });
  it("keeps loaded sources on refresh failure and supports retry", async () => {
    list
      .mockResolvedValueOnce({
        data: { items: [migrationSource], nextCursor: "" },
      } as never)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue({
        data: { items: [migrationSource], nextCursor: "" },
      } as never);
    show("");
    await screen.findByRole("button", { name: /Build Nexus/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Refresh source" }),
    );
    await screen.findByText(/request did not complete/);
    expect(
      screen.getByRole("button", { name: /Build Nexus/ }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Retry/ }));
    await waitFor(() =>
      expect(
        screen.queryByText(/request did not complete/),
      ).not.toBeInTheDocument(),
    );
  });
  it("loads a source deep link separately from list pagination", async () => {
    list
      .mockResolvedValueOnce({
        data: { items: [], nextCursor: "opaque-cursor" },
      } as never)
      .mockResolvedValue({
        data: { items: [migrationSource], nextCursor: "" },
      } as never);
    show();
    await screen.findByRole("button", { name: "Run preflight" });
    expect(detail).toHaveBeenCalledWith(
      expect.objectContaining({ path: { sourceId: migrationSource.id } }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Load more migration sources" }),
    );
    await waitFor(() =>
      expect(list).toHaveBeenCalledWith(
        expect.objectContaining({
          query: { limit: 100, cursor: "opaque-cursor" },
        }),
      ),
    );
    expect(
      await screen.findByRole("button", { name: /Build Nexus/ }),
    ).toBeInTheDocument();
  });
  it("discovers only explicitly selected supported Hosted repositories", async () => {
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    expect(
      await screen.findByRole("checkbox", { name: "Select snapshots" }),
    ).toBeDisabled();
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Select releases" }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Select central" }),
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Discover selected Hosted content (1)",
      }),
    );
    const result = await screen.findByRole("region", {
      name: "Source asset discovery",
    });
    await within(result).findByText(/2 source assets/);
    expect(discover).toHaveBeenCalledWith(
      expect.objectContaining({ body: { repositories: ["releases"] } }),
    );
    expect(result).toHaveTextContent("No bytes have");
    expect(result).toHaveTextContent(
      "Shared Maven metadata or paths without a single release version",
    );
    expect(result).not.toHaveTextContent("100%");
  });
  it("preserves the previous discovery on a failed reread without partial success", async () => {
    discover
      .mockResolvedValueOnce({ data: migrationDiscovery } as never)
      .mockResolvedValue({
        error: {
          status: 502,
          code: "source_limit_exceeded",
          message: "secret",
        },
      } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await userEvent.click(
      await screen.findByRole("checkbox", { name: "Select releases" }),
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Discover selected Hosted content (1)",
      }),
    );
    const result = await screen.findByRole("region", {
      name: "Source asset discovery",
    });
    await within(result).findByText(/2 source assets/);
    await userEvent.click(
      screen.getByRole("button", {
        name: "Discover selected Hosted content (1)",
      }),
    );
    expect(await within(result).findByRole("alert")).toHaveTextContent(
      "safety budget",
    );
    expect(result).toHaveTextContent("Keeping the last successful result");
    expect(result).toHaveTextContent("2 source assets");
    expect(result).not.toHaveTextContent("secret");
  });
  it("previews an npm Hosted mapping using its native format", async () => {
    preflight.mockResolvedValueOnce({
      data: {
        ...migrationPreflight,
        repositories: [
          { ...migrationPreflight.repositories[0], format: "npm" },
        ],
      },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await userEvent.click(
      await screen.findByRole("checkbox", { name: "Select releases" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "Target name for releases" }),
      "native-npm",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Preview explicit mappings" }),
    );
    await waitFor(() =>
      expect(preview).toHaveBeenCalledWith(
        expect.objectContaining({
          body: {
            mappings: [
              {
                sourceRepository: "releases",
                targetName: "native-npm",
                format: "npm",
                type: "hosted",
              },
            ],
          },
        }),
      ),
    );
  });
  it("builds explicit multi-repository mappings with source Group order", async () => {
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    for (const name of ["public", "central", "releases"]) {
      await userEvent.click(
        await screen.findByRole("checkbox", { name: `Select ${name}` }),
      );
      await userEvent.type(
        screen.getByRole("textbox", { name: `Target name for ${name}` }),
        `native-${name}`,
      );
    }
    await userEvent.click(
      screen.getByRole("button", { name: "Preview explicit mappings" }),
    );
    const result = await screen.findByRole("region", {
      name: "Mapping preview results",
    });
    await within(result).findByText(
      /Selected mappings pass current validation\. No targets have been created and no content has migrated\./,
    );
    expect(preview).toHaveBeenCalledWith(
      expect.objectContaining({
        body: {
          mappings: [
            {
              sourceRepository: "releases",
              targetName: "native-releases",
              format: "maven",
              type: "hosted",
            },
            {
              sourceRepository: "central",
              targetName: "native-central",
              format: "maven",
              type: "proxy",
            },
            {
              sourceRepository: "public",
              targetName: "native-public",
              format: "maven",
              type: "group",
              targetMembers: ["native-releases", "native-central"],
            },
          ],
        },
      }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "Target name for releases" }),
      "-changed",
    );
    expect(
      screen.queryByRole("region", { name: "Mapping preview results" }),
    ).not.toBeInTheDocument();
  });
  it("rejects a response from a changed connection version", async () => {
    preflight.mockResolvedValue({
      data: { ...migrationPreflight, sourceVersion: "2" },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByText(/source connection version changed/);
    expect(
      screen.queryByRole("checkbox", { name: "Select releases" }),
    ).not.toBeInTheDocument();
  });
  it("removes a previous observation when administrator access is refused", async () => {
    preflight
      .mockResolvedValueOnce({ data: migrationPreflight } as never)
      .mockResolvedValue({
        error: { status: 403, message: "hidden-role-detail" },
      } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByRole("checkbox", { name: "Select releases" });
    expect(
      screen.getAllByText(
        "Source configuration and asset metadata are readable. No content has migrated.",
      ).length,
    ).toBe(2);
    await userEvent.click(
      screen.getByRole("button", { name: "Run preflight" }),
    );
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    expect(
      screen.queryByRole("checkbox", { name: "Select releases" }),
    ).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("hidden-role-detail");
  });
  it("does not show a late observation after changing the selected source", async () => {
    const second = {
      ...migrationSource,
      id: "cb7de0a3-79f3-4cf0-9d5e-d29bf5bd1a8c",
      name: "Second source",
    };
    list.mockResolvedValue({
      data: { items: [migrationSource, second], nextCursor: "" },
    } as never);
    detail.mockImplementation(
      async (options) =>
        ({
          data: options.path.sourceId === second.id ? second : migrationSource,
        }) as never,
    );
    let finish!: (value: unknown) => void;
    preflight.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }) as never,
    );
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    const signal = preflight.mock.calls[0][0].signal;
    await userEvent.click(
      screen.getByRole("button", { name: /Second source/ }),
    );
    await screen.findByRole("heading", { name: "Second source" });
    expect(signal?.aborted).toBe(true);
    finish({ data: migrationPreflight });
    expect(
      screen.queryByRole("checkbox", { name: "Select releases" }),
    ).not.toBeInTheDocument();
  });
  it("uses If-Match for deletion and retains the source on a stale version", async () => {
    vi.mocked(deleteMigrationSource).mockResolvedValue({
      error: { status: 412, code: "version_conflict" },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Delete source" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Delete migration source?",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Delete source" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Another operation updated this connection",
    );
    expect(vi.mocked(deleteMigrationSource)).toHaveBeenCalledWith({
      path: { sourceId: migrationSource.id },
      headers: { "If-Match": "1" },
    });
    expect(
      screen.getByRole("heading", { name: "Build Nexus" }),
    ).toBeInTheDocument();
  });
});
