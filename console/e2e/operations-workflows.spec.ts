import { expect, test, type Page } from "@playwright/test";
import { openSelect, waitForModalOpen } from "./support/motion";

/**
 * Operations against a live Gateway: each tab loads on its own, including
 * when the page is opened directly on it.
 */

const token = process.env.NOTARX_ADMIN_TOKEN;

test.skip(!token, "NOTARX_ADMIN_TOKEN is required for the operations gate");

async function authenticate(page: Page) {
  await page.addInitScript((accessToken) => {
    localStorage.setItem("ag.console.token", accessToken);
    localStorage.setItem("ag.console.role", "admin");
  }, token!);
}

test("a direct link to job history loads the jobs", async ({ page }) => {
  await authenticate(page);
  const jobs = page.waitForResponse(
    (response) =>
      response.url().includes("/api/v2/lifecycle-jobs") && response.ok(),
  );

  await page.goto("/operations?tab=jobs");

  await jobs;
  await expect(page.getByRole("tab", { name: "Job history" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByText("Loading job status…")).toHaveCount(0);
  await expect(
    page
      .getByText("No matching jobs")
      .or(page.locator(".ag-operation-desktop-table .ant-table-row").first()),
  ).toBeVisible();
});

const suffix = `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
const admin = () => ({ Authorization: `Bearer ${token!}` });

test("a scheduled task is created, paused, run, and deleted", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const name = `e2e-schedule-${suffix}`;
  const repositoryName = `e2e-schedule-target-${suffix}`;
  const createdRepository = await request.post("/api/v2/repositories", {
    headers: { ...admin(), "Idempotency-Key": `e2e-schedule-target-${suffix}` },
    data: { name: repositoryName, format: "raw", type: "hosted" },
  });
  expect(createdRepository.status()).toBe(201);
  const repository = (await createdRepository.json()) as { id: string };
  const stored = async () => {
    const response = await request.get("/api/v2/scheduled-tasks", {
      headers: admin(),
    });
    expect(response.ok()).toBeTruthy();
    return (
      (await response.json()) as Array<{
        id: string;
        name: string;
        enabled: boolean;
        kind: string;
        repositoryId?: string;
      }>
    ).find((task) => task.name === name);
  };

  try {
    // An owned target avoids depending on the global audit-retention policy,
    // which another browser scenario can legitimately change concurrently.
    const policyURL = `/api/v2/repositories/${repository.id}/retention-policy`;
    const currentPolicy = await request.get(policyURL, { headers: admin() });
    expect(currentPolicy.ok()).toBeTruthy();
    const policy = (await currentPolicy.json()) as { version: string };
    const enable = await request.put(policyURL, {
      headers: { ...admin(), "If-Match": policy.version },
      data: { ...policy, enabled: true, keepDays: 1 },
    });
    expect(enable.ok()).toBeTruthy();

    await page.goto("/operations?tab=schedules");
    await page.getByRole("button", { name: /New schedule/ }).click();
    const dialog = page.getByRole("dialog", { name: "New scheduled task" });
    await waitForModalOpen(dialog);
    await dialog.getByPlaceholder("e.g. Daily snapshot cleanup").fill(name);
    const repositoryDropdown = await openSelect(
      page,
      dialog.getByRole("combobox").nth(1),
    );
    await repositoryDropdown
      .locator(".ant-select-item-option")
      .filter({ hasText: repositoryName })
      .click();
    await dialog.getByRole("button", { name: /^Create$/ }).click();
    await expect(dialog).toBeHidden();
    await expect
      .poll(async () => (await stored())?.kind)
      .toBe("repository-retention");
    expect((await stored())?.repositoryId).toBe(repository.id);

    const row = page.getByRole("row").filter({ hasText: name });
    await row.getByRole("switch", { name: "Enable scheduled task" }).click();
    await expect.poll(async () => (await stored())?.enabled).toBe(false);

    // A manual run is recorded in the task's dispatch history.
    await row.getByRole("button", { name: "Dispatch history" }).click();
    const runButton = row.getByRole("button", { name: "Run now" });
    await expect(runButton).not.toHaveClass(/ant-btn-loading/);
    const dispatch = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/api\/v2\/scheduled-tasks\/[^/]+\/run$/.test(
          new URL(response.url()).pathname,
        ),
    );
    await runButton.click();
    const dispatched = await dispatch;
    expect(dispatched.status()).toBe(202);
    expect(await dispatched.json()).toMatchObject({
      trigger: "manual",
      state: "submitted",
    });
    await expect(
      page.getByText("Manual", { exact: true }).first(),
    ).toBeVisible();

    await row.getByRole("button", { name: `More actions for ${name}` }).click();
    await page.getByRole("menuitem", { name: "Delete schedule" }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: /^Delete$/ })
      .click();
    await expect(row).toHaveCount(0);
    expect(await stored()).toBeUndefined();
  } finally {
    const task = await stored();
    if (task) {
      const removed = await request.delete(
        `/api/v2/scheduled-tasks/${task.id}`,
        { headers: admin() },
      );
      expect(removed.ok()).toBeTruthy();
    }
    const removed = await request.delete(
      `/api/v2/repositories/${repository.id}`,
      { headers: admin() },
    );
    expect(removed.status()).toBe(202);
  }
});

test("a webhook subscription is created and paused", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const name = `e2e-webhook-${suffix}`;
  const stored = async () => {
    // Subscriptions page by name: follow the cursor until the name is found.
    let pageToken: string | undefined;
    do {
      const response = await request.get("/api/v2/webhook-subscriptions", {
        headers: admin(),
        params: pageToken ? { pageToken } : {},
      });
      expect(response.ok()).toBeTruthy();
      const page = (await response.json()) as {
        items: Array<{ name: string; enabled: boolean }>;
        nextPageToken?: string;
      };
      const found = page.items.find(
        (subscription) => subscription.name === name,
      );
      if (found) return found;
      pageToken = page.nextPageToken;
    } while (pageToken);
    return undefined;
  };

  await page.goto("/operations?tab=webhooks");
  await page.getByRole("button", { name: "New subscription" }).click();
  const dialog = page.getByRole("dialog", { name: "New webhook subscription" });
  await waitForModalOpen(dialog);
  await dialog.getByPlaceholder("e.g. Security automation").fill(name);
  await dialog
    .getByPlaceholder("https://hooks.example.com/artifacts")
    .fill("https://hooks.example.test/artifacts");
  await dialog.getByPlaceholder("At least 32 characters").fill("s".repeat(40));
  await dialog.getByRole("button", { name: "Create" }).click();
  // Signing secrets are stored encrypted; a Gateway started without
  // NOTARX_SETTINGS_ENCRYPTION_KEY refuses them with a stable code.
  const refused = dialog.getByText("encryption_key_unavailable");
  await expect(
    refused.or(page.getByRole("row").filter({ hasText: name })),
  ).toBeVisible();
  test.skip(
    await refused.isVisible(),
    "The Gateway has no NOTARX_SETTINGS_ENCRYPTION_KEY for webhook secrets.",
  );
  await expect(dialog).toBeHidden();
  await expect.poll(async () => (await stored())?.enabled).toBe(true);

  await page
    .getByRole("row")
    .filter({ hasText: name })
    .getByRole("switch", { name: "Enable webhook subscription" })
    .click();
  await expect.poll(async () => (await stored())?.enabled).toBe(false);
});
