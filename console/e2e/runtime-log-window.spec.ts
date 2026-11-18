import { expect, test } from "@playwright/test";
import { authenticateAsAdmin } from "./support/auth";
import { defaultSiteSettings } from "../src/lib/siteSettings";

test.use({ timezoneId: "Asia/Shanghai" });

test("rejects a 24-hour-plus-one-second custom window before requesting logs", async ({
  page,
}) => {
  await authenticateAsAdmin(page);
  await page.route("**/api/v2/site-settings", (route) =>
    route.fulfill({ json: defaultSiteSettings }),
  );
  const requests: string[] = [];
  await page.route("**/api/v2/runtime/logs**", (route) => {
    requests.push(route.request().url());
    return route.fulfill({
      json: {
        scope: "local",
        instanceId: "synthetic",
        sessionId: "session",
        items: [],
        afterCursor: "cursor",
      },
    });
  });
  await page.goto("/system?tab=logs");
  await expect(page.getByText("No logs in this range")).toBeVisible();
  const custom = page.getByRole("combobox", {
    name: "Time range",
    exact: true,
  });
  if (await custom.count()) {
    await custom.click();
    await page.getByRole("option", { name: "Custom", exact: true }).click();
  }
  await page.getByPlaceholder("Start date").fill("2026-10-02 22:07:26");
  await page.getByPlaceholder("Start date").press("Enter");
  await page.getByPlaceholder("End date").fill("2026-10-03 22:07:27");
  await page.getByPlaceholder("End date").press("Enter");
  await page.getByPlaceholder("End date").press("Escape");
  const count = requests.length;
  await page.getByRole("button", { name: /Search$/ }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Time range cannot exceed 24 hours",
  );
  expect(requests.length).toBe(count);
});
