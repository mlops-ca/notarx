import { expect, test, type Page } from "@playwright/test";
import { authenticateAsAdmin } from "./support/auth";

// fr-CA smoke test (ADR 0014): the Console follows the browser language, sets
// the document language, shows French text and formats dates in fr-CA.

async function mockAudit(page: Page) {
  await authenticateAsAdmin(page);
  await page.route("**/api/v2/repositories**", (route) =>
    route.fulfill({ json: { items: [] } }),
  );
  await page.route("**/api/v2/groups**", (route) =>
    route.fulfill({ json: { items: [] } }),
  );
  await page.route("**/api/v2/audits/page**", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            outcome: "resolved",
            occurredAt: "2026-10-14T15:04:05Z",
            operation: "read",
            repository: "demo",
            format: "maven",
            actor: "fixture-admin",
          },
        ],
      },
    }),
  );
}

async function expectFrench(page: Page) {
  await page.goto("/audits");
  await expect(page.locator("html")).toHaveAttribute("lang", "fr-CA");
  await expect(
    page.getByRole("heading", { name: "Journal d’audit" }),
  ).toBeVisible();
  // fr-CA writes the time as "15 h 04 min 05 s" (Intl adds no-break spaces).
  await expect(page.locator(".ant-table-tbody")).toContainText(
    /2026-10-14\s+15\s*h\s*04\s*min\s*05\s*s/,
  );
}

test.describe("with an Accept-Language header of fr-CA", () => {
  test.use({
    locale: "fr-CA",
    timezoneId: "UTC",
    extraHTTPHeaders: { "Accept-Language": "fr-CA" },
  });

  test("loads the Console in French", async ({ page }) => {
    const headers: (string | undefined)[] = [];
    page.on("request", (request) => {
      if (request.url().endsWith("/audits")) {
        headers.push(request.headers()["accept-language"]);
      }
    });
    await mockAudit(page);
    await expectFrench(page);
    expect(headers[0]).toMatch(/^fr-CA/);
  });
});

test.describe("with fr-CA in navigator.languages", () => {
  test.use({ locale: "en-CA", timezoneId: "UTC" });

  test("loads the Console in French", async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "languages", {
        configurable: true,
        get: () => ["fr-CA", "en"],
      });
    });
    await mockAudit(page);
    await expectFrench(page);
  });
});
