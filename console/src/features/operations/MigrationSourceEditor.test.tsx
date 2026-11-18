import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMigrationSource, updateMigrationSource } from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { migrationSource } from "../../test/migrationFixtures";
import { MigrationSourceEditor } from "./MigrationSourceEditor";
import { TestQueryProvider } from "../../test/queryClient";

vi.mock("../../client", () => ({
  createMigrationSource: vi.fn(),
  updateMigrationSource: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
function editor(edit = true) {
  const onSaved = vi.fn();
  const onClose = vi.fn();
  const onReload = vi.fn().mockResolvedValue(undefined);
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <MigrationSourceEditor
            source={edit ? migrationSource : null}
            onSaved={onSaved}
            onClose={onClose}
            onReload={onReload}
          />
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
  return { onSaved, onClose, onReload };
}
async function choose(label: string) {
  await userEvent.click(
    screen.getByRole("combobox", { name: "Source credentials" }),
  );
  await userEvent.click(await screen.findByRole("option", { name: label }));
}
describe("migration source credentials", () => {
  it("retains existing credentials by omission and writes with If-Match", async () => {
    vi.mocked(updateMigrationSource).mockResolvedValue({
      data: { ...migrationSource, version: "2" },
    } as never);
    const { onSaved } = editor();
    expect(screen.queryByLabelText("Source password")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Save migration source" }),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(updateMigrationSource).toHaveBeenCalledWith({
      path: { sourceId: migrationSource.id },
      headers: { "If-Match": "1" },
      body: { name: migrationSource.name, baseUrl: migrationSource.baseUrl },
    });
  });
  it("clears credentials explicitly without sending empty secret fields", async () => {
    vi.mocked(updateMigrationSource).mockResolvedValue({
      data: { ...migrationSource, credentialsConfigured: false, version: "2" },
    } as never);
    const { onSaved } = editor();
    await choose("Clear current credentials");
    await userEvent.click(
      screen.getByRole("button", { name: "Save migration source" }),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(updateMigrationSource).toHaveBeenCalledWith(
      expect.objectContaining({
        body: {
          name: migrationSource.name,
          baseUrl: migrationSource.baseUrl,
          clearCredentials: true,
        },
      }),
    );
  });
  it("clears submitted credentials on rejection and never renders an echoed secret", async () => {
    vi.mocked(createMigrationSource).mockRejectedValue(
      new Error("server echoed source-password"),
    );
    editor(false);
    await userEvent.type(
      screen.getByRole("textbox", { name: "Source name" }),
      "Test source",
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "Nexus HTTPS address" }),
      "https://nexus.example.com",
    );
    await choose("Set new credentials");
    await userEvent.type(
      screen.getByRole("textbox", { name: "Source username" }),
      "source-user",
    );
    await userEvent.type(
      screen.getByLabelText("Source password", { selector: "input" }),
      "source-password",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Create migration source" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "request did not complete",
    );
    expect(
      screen.getByRole("textbox", { name: "Source username" }),
    ).toHaveValue("");
    expect(
      screen.getByLabelText("Source password", { selector: "input" }),
    ).toHaveValue("");
    expect(
      screen.getByRole("button", { name: "Create migration source" }),
    ).toBeDisabled();
    expect(document.body).not.toHaveTextContent("source-password");
    expect(localStorage.getItem("source-password")).toBeNull();
  });
  it("requires replacing or clearing credentials when the address changes", async () => {
    editor();
    const address = screen.getByRole("textbox", {
      name: "Nexus HTTPS address",
    });
    await userEvent.clear(address);
    await userEvent.type(address, "https://other.example.com");
    expect(
      screen.getByRole("button", { name: "Save migration source" }),
    ).toBeDisabled();
    await choose("Clear current credentials");
    expect(
      screen.getByRole("button", { name: "Save migration source" }),
    ).toBeEnabled();
  });
  it("offers an explicit reread after a version conflict", async () => {
    vi.mocked(updateMigrationSource).mockResolvedValue({
      error: { status: 412, message: "hidden-server-data" },
    } as never);
    const { onReload } = editor();
    await userEvent.click(
      screen.getByRole("button", { name: "Save migration source" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Read the latest version",
    );
    expect(document.body).not.toHaveTextContent("hidden-server-data");
    await userEvent.click(
      screen.getByRole("button", { name: "Read latest version" }),
    );
    await waitFor(() => expect(onReload).toHaveBeenCalledOnce());
  });
});
