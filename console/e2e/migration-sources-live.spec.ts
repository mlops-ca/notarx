import { expect, test } from "@playwright/test";
import type { MigrationSource } from "../src/client";
import { waitForModalOpen, openSelect } from "./support/motion";

const token = process.env.NOTARX_ADMIN_TOKEN;
const sourceUrl = process.env.MIGRATION_SOURCE_FIXTURE_URL;
test.skip(
  !token || !sourceUrl,
  "Requires an isolated live Gateway and approved TLS Nexus fixture",
);

test("source connection, metadata reads, explicit mapping and credential lifecycle use the real Gateway", async ({
  page,
  request,
}) => {
  await page.addInitScript((token) => {
    localStorage.setItem("ag.console.token", token);
    localStorage.setItem("ag.console.role", "admin");
    localStorage.setItem("ag.console.locale", "en-CA");
  }, token!);
  const headers = { Authorization: `Bearer ${token!}` };
  const name = `browser-source-${Date.now()}`;
  let source: MigrationSource | undefined;
  const read = async () => {
    const response = await request.get(
      `/api/v2/migration-sources/${source!.id}`,
      { headers },
    );
    expect(response.ok()).toBeTruthy();
    const text = await response.text();
    expect(text).not.toContain("fixture-reader");
    expect(text).not.toContain("fixture-password");
    expect(text).not.toContain('credentials"');
    expect(text).not.toContain("credentialCiphertext");
    source = JSON.parse(text) as MigrationSource;
    return source;
  };
  try {
    await page.goto("/operations?tab=migrations");
    await page.getByRole("button", { name: "Add migration source" }).click();
    const dialog = page.getByRole("dialog", { name: "Add migration source" });
    await waitForModalOpen(dialog);
    await dialog.getByRole("textbox", { name: "Source name" }).fill(name);
    await dialog
      .getByRole("textbox", { name: "Nexus HTTPS address" })
      .fill(sourceUrl!);
    const newCredentials = await openSelect(
      page,
      dialog.getByRole("combobox", { name: "Source credentials" }),
    );
    await newCredentials
      .getByRole("option", { name: "Set new credentials" })
      .click();
    await dialog
      .getByRole("textbox", { name: "Source username" })
      .fill("fixture-reader");
    await dialog
      .getByLabel("Source password", { exact: true })
      .fill("fixture-password");
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/v2/migration-sources",
    );
    await dialog
      .getByRole("button", { name: "Create migration source" })
      .click();
    const createdResponse = await created;
    expect(createdResponse.status()).toBe(201);
    source = (await createdResponse.json()) as MigrationSource;
    await expect(dialog).toBeHidden();
    expect((await read()).credentialsConfigured).toBe(true);
    await expect(page).toHaveURL(new RegExp(`source=${source.id}`));
    await page.getByRole("button", { name: "Run preflight" }).click();
    await expect(
      page.getByRole("checkbox", { name: "Select snapshots", exact: true }),
    ).toBeDisabled();
    for (const repository of ["releases", "raw-source"]) {
      await page
        .getByRole("checkbox", { name: `Select ${repository}`, exact: true })
        .check();
      await page
        .getByRole("textbox", { name: `Target name for ${repository}` })
        .fill(`browser-${repository}`);
    }
    await page
      .getByRole("button", { name: "Discover selected Hosted content (2)" })
      .click();
    await expect(page.getByText(/2 source assets/)).toBeVisible();
    await expect(
      page.getByText("exports/build+1.txt", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Preview explicit mappings" })
      .click();
    await expect(
      page.getByText(
        /Selected mappings pass current validation\. No targets have been created and no content has migrated\./,
      ),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Preflight has not run", { exact: true }),
    ).toBeVisible();

    await page.getByRole("button", { name: "Edit source" }).click();
    const edit = page.getByRole("dialog", { name: "Edit migration source" });
    await waitForModalOpen(edit);
    await expect(edit.locator('input[type="password"]')).toHaveCount(0);
    const editCredentials = await openSelect(
      page,
      edit.getByRole("combobox", { name: "Source credentials" }),
    );
    await editCredentials
      .getByRole("option", { name: "Set new credentials" })
      .click();
    await edit
      .getByRole("textbox", { name: "Source username" })
      .fill("fixture-reader");
    await edit
      .getByLabel("Source password", { exact: true })
      .fill("fixture-password-rotated");
    await edit.getByRole("button", { name: "Save migration source" }).click();
    await expect(edit).toBeHidden();
    expect((await read()).version).toBe("2");
    await page.getByRole("button", { name: "Run preflight" }).click();
    await expect(
      page.getByRole("checkbox", { name: "Select releases", exact: true }),
    ).toBeVisible();

    await page.getByRole("button", { name: "Edit source" }).click();
    await waitForModalOpen(edit);
    const clearCredentials = await openSelect(
      page,
      edit.getByRole("combobox", { name: "Source credentials" }),
    );
    await clearCredentials
      .getByRole("option", { name: "Clear current credentials" })
      .click();
    await edit.getByRole("button", { name: "Save migration source" }).click();
    await expect(edit).toBeHidden();
    expect((await read()).credentialsConfigured).toBe(false);
    await page.getByRole("button", { name: "Run preflight" }).click();
    await expect(
      page.getByText(
        /Source access was denied\. Check credentials, read-only permissions and the server egress allowlist\./,
      ),
    ).toBeVisible();
    await expect(page.locator("body")).not.toContainText("fixture-password");
    expect(
      await page.evaluate(() =>
        JSON.stringify({
          local: { ...localStorage },
          session: { ...sessionStorage },
        }),
      ),
    ).not.toMatch(/fixture-reader|fixture-password/);
    const resolver = process.env.NOTARX_RESOLVER_TOKEN;
    if (resolver)
      expect(
        (
          await request.get("/api/v2/migration-sources", {
            headers: { Authorization: `Bearer ${resolver}` },
          })
        ).status(),
      ).toBe(403);
    await page
      .getByRole("button", { name: "Delete source", exact: true })
      .click();
    await page
      .getByRole("dialog", { name: "Delete migration source?" })
      .getByRole("button", { name: "Delete source", exact: true })
      .click();
    await expect(page.getByRole("heading", { name, exact: true })).toHaveCount(
      0,
    );
    expect(
      (
        await request.get(`/api/v2/migration-sources/${source.id}`, { headers })
      ).status(),
    ).toBe(404);
    source = undefined;
  } finally {
    if (source) {
      const current = await read();
      await request.delete(`/api/v2/migration-sources/${current.id}`, {
        headers: { ...headers, "If-Match": current.version },
      });
    }
  }
});
