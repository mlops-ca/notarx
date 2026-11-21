import { expect, test, type Locator, type Page } from "@playwright/test";
import { defaultConsoleThemes } from "../src/lib/consoleTheme";
import { authenticateAsAdmin } from "./support/auth";
import { expectTabGutter } from "./support/tabs";

function captureRuntimeErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text());
  });
  return errors;
}

async function mockAccessControl(page: Page) {
  await authenticateAsAdmin(page);
  await page.addInitScript(() => {
    localStorage.setItem("ag.console.theme", "light");
  });
  await page.route("**/api/v2/site-settings", (route) =>
    route.fulfill({
      json: {
        version: 1,
        siteName: "notarx",
        logoUrl: "",
        brandMark: "NX",
        enabledThemeIds: ["notarx-dark", "notarx-light"],
        defaultThemeId: "notarx-dark",
        availableThemes: defaultConsoleThemes,
      },
    }),
  );
  await page.route("**/api/v2/repository-grants**", (route) =>
    route.fulfill({ json: [] }),
  );
  await page.route("**/api/v2/public-access-policy**", (route) =>
    route.fulfill({ json: { enabled: true, version: "7" } }),
  );
  await page.route("**/api/v2/user-groups**", (route) =>
    route.fulfill({ json: { items: [], limit: 100 } }),
  );
  await page.route("**/api/v2/users**", (route) =>
    route.fulfill({ json: { items: [] } }),
  );
  await page.route("**/api/v2/api-keys**", (route) =>
    route.fulfill({ json: { items: [] } }),
  );
  await page.route("**/api/v2/service-accounts**", (route) =>
    route.fulfill({ json: { items: [] } }),
  );
  await page.route("**/api/v2/repositories**", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "repo-public",
            name: "public-releases",
            format: "raw",
            type: "hosted",
            state: "active",
            visibility: "public",
          },
        ],
      },
    }),
  );
  await page.route("**/api/v2/authorization-roles**", (route) =>
    route.fulfill({ json: [] }),
  );
  await page.route("**/api/v2/authorization-templates**", (route) =>
    route.fulfill({ json: [] }),
  );
}

async function verticalGap(upper: Locator, lower: Locator) {
  const [upperBox, lowerBox] = await Promise.all([
    upper.boundingBox(),
    lower.boundingBox(),
  ]);
  if (!upperBox || !lowerBox) return -1;
  return Math.round(lowerBox.y - (upperBox.y + upperBox.height));
}

async function horizontalOverflow(page: Page) {
  return page
    .locator("html")
    .evaluate((element) =>
      Math.max(0, element.scrollWidth - element.clientWidth),
    );
}

async function expectCompactBoundary(page: Page, maxHeight: number) {
  const card = page.locator(".ag-public-access-card");
  await expect(
    card.getByRole("heading", { name: "Public access boundary" }),
  ).toBeVisible();
  await expect(
    card.getByText("1 / 1 repositories public", { exact: true }),
  ).toBeVisible();
  await expect(
    card.getByRole("switch", { name: "Toggle global anonymous reads" }),
  ).toBeChecked();
  await expect(
    card.getByText(
      "Anonymous reads require global, repository, and applicable group opt-in; writes, deletes, and management still require authentication.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(card.locator(".ant-alert-info")).toHaveCount(1);
  const box = await card.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.height).toBeLessThanOrEqual(maxHeight);
  for (const [upper, lower, minimum, maximum] of [
    [page.locator(".ag-page-header"), page.locator(".ag-metric-strip"), 24, 26],
    [page.locator(".ag-metric-strip"), page.locator(".ag-access-tabs"), 16, 18],
    [card, card.locator("xpath=following-sibling::*[1]"), 16, 18],
  ] as const) {
    await expect
      .poll(() => verticalGap(upper, lower))
      .toBeGreaterThanOrEqual(minimum);
    await expect
      .poll(() => verticalGap(upper, lower))
      .toBeLessThanOrEqual(maximum);
  }
  await expectTabGutter(page, ".ag-access-tabs", ".ag-card");
  expect(await horizontalOverflow(page)).toBe(0);
  return card;
}

test("compact public access controls use coherent light and dark themes", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const runtimeErrors = captureRuntimeErrors(page);
  await mockAccessControl(page);
  await page.goto("/access?tab=policies");
  const card = await expectCompactBoundary(page, 220);
  const heading = card.getByRole("heading", { name: "Public access boundary" });
  await expect(heading).toHaveCSS("color", "rgb(18, 20, 14)");
  const navigation = await page.locator(".ag-sider-desktop").boundingBox();
  const cardBox = await card.boundingBox();
  expect(navigation).not.toBeNull();
  expect(cardBox!.x).toBeGreaterThanOrEqual(
    navigation!.x + navigation!.width + 23,
  );
  expect(runtimeErrors).toEqual([]);
  await page.screenshot({
    path: testInfo.outputPath("access-control-light-viewport.png"),
  });

  await page.getByRole("button", { name: /notarx Light/ }).click();
  await page.getByRole("menuitem", { name: /notarx Dark/ }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(heading).toHaveCSS("color", "rgb(246, 245, 238)");
  await expectCompactBoundary(page, 220);
  expect(runtimeErrors).toEqual([]);
  await page.screenshot({
    path: testInfo.outputPath("access-control-dark-viewport.png"),
  });
});

test("compact public access controls keep bounded mobile rhythm", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const runtimeErrors = captureRuntimeErrors(page);
  await mockAccessControl(page);
  await page.goto("/access?tab=policies");
  await expectCompactBoundary(page, 260);
  expect(runtimeErrors).toEqual([]);
  await page.screenshot({
    path: testInfo.outputPath("access-control-mobile.png"),
    fullPage: true,
  });
});

test("permission evaluation has one collapsed explanation entry", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const runtimeErrors = captureRuntimeErrors(page);
  await mockAccessControl(page);
  await page.goto("/access?tab=evaluate");
  const explanation = page.getByRole("button", {
    name: "Authorization order and roles",
  });
  await expect(explanation).toHaveCount(1);
  await expect(explanation).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByText(/^1\.\s*Identify the principal$/)).toBeHidden();
  await explanation.click();
  await expect(page.getByText(/^1\.\s*Identify the principal$/)).toHaveCount(1);
  expect(await horizontalOverflow(page)).toBe(0);
  expect(runtimeErrors).toEqual([]);
});
