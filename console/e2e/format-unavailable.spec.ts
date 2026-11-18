import { expect, test } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import { mockGateway } from "./visual/mockGateway";
import { mavenReleases } from "./visual/fixtures";

for (const width of [1440, 375]) {
  for (const surface of ["repository", "public"] as const) {
    test(`unregistered format is unavailable on ${surface} at ${width}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error") errors.push(message.text());
      });
      const mock = await mockGateway(page, {
        authenticated: surface === "repository",
      });
      if (surface === "repository") {
        await page.route(
          `**/api/v2/repositories/${mavenReleases.id}`,
          (route) =>
            route.fulfill({ json: { ...mavenReleases, format: "nuget" } }),
        );
        await page.goto(`/repositories/${mavenReleases.id}`);
      } else {
        await page.route("**/api/v2/public/repositories", (route) =>
          route.fulfill({
            json: {
              enabled: true,
              items: [
                {
                  id: "future",
                  name: "nuget-public",
                  format: "nuget",
                  type: "proxy",
                },
              ],
            },
          }),
        );
        await page.goto("/browse");
        await page.getByRole("link", { name: /nuget-public/ }).click();
      }
      const unavailable = page.getByText(
        "This Console does not support the nuget format yet",
        {
          exact: true,
        },
      );
      await expect(unavailable).toBeVisible();
      await expect(page.getByText("No artifacts", { exact: true })).toHaveCount(
        0,
      );
      await expect(
        page.getByText("Raw repository address", { exact: true }),
      ).toHaveCount(0);
      await expect(
        page.getByText(
          "The current backend does not expose this endpoint (404)",
        ),
      ).toHaveCount(0);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
      const box = await unavailable.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      expect(errors).toEqual([]);
      expect(mock.unmatched).toEqual([]);
      await mkdir("../output/playwright", { recursive: true });
      await page.screenshot({
        path: `../output/playwright/format-unavailable-${surface}-${width}.png`,
        fullPage: true,
      });
    });
  }
}
