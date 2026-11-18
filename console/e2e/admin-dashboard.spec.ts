import { expect, test } from "@playwright/test";

const token = process.env.NOTARX_ADMIN_TOKEN;

test.skip(
  !token,
  "NOTARX_ADMIN_TOKEN is required for the administrator browser gate",
);

test("administrator dashboard loads management data through the Console proxy", async ({
  page,
}) => {
  await page.addInitScript(
    ([accessToken, role]) => {
      localStorage.setItem("ag.console.token", accessToken);
      localStorage.setItem("ag.console.role", role);
    },
    [token!, "admin"],
  );

  const statistics = page.waitForResponse(
    (response) =>
      response.url().includes("/api/v2/overview-statistics") &&
      response.status() === 200,
  );
  await page.goto("/");
  await statistics;
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByText("Repositories")).toBeVisible();
});
