import { expect, test } from "@playwright/test";
import { authenticateWithIdentity } from "./support/auth";

test("server identity overrides a stale administrator role", async ({
  page,
}) => {
  await authenticateWithIdentity(page, {
    actor: "gateway-resolver",
    kind: "static_resolver",
    administrator: false,
  });

  await page.goto("/access");

  await expect(page).toHaveURL(/\/search$/);
  await expect(
    page.getByRole("heading", { name: "Global search" }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Artifact Search" }),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "Access control" })).toHaveCount(
    0,
  );
  await expect(page.getByRole("link", { name: "API keys" })).toHaveCount(0);
});

test("a member sees the repository catalog without administrator navigation", async ({
  page,
}) => {
  await authenticateWithIdentity(page, {
    actor: "user:member",
    kind: "local_session",
    role: "member",
    administrator: false,
  });
  await page.route("**/api/v2/repositories?**", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "11111111-1111-1111-1111-111111111111",
            name: "readable-releases",
            format: "raw",
            type: "hosted",
            anonymousRead: false,
            mavenStrictPublication: false,
            state: "active",
            version: "1",
          },
        ],
      },
    }),
  );

  await page.goto("/repositories");

  await expect(page).toHaveURL(/\/repositories$/);
  await expect(
    page.getByRole("link", { name: "readable-releases" }),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "Access control" })).toHaveCount(
    0,
  );
  await expect(page.getByRole("link", { name: "API keys" })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /New repository/ }),
  ).toHaveCount(0);
});

test("a scoped token signs in through identity without repository discovery", async ({
  page,
}) => {
  let repositoryDiscoveryRequests = 0;
  await page.route("**/api/v2/identity", (route) =>
    route.fulfill({
      json: {
        actor: "gateway-resolver",
        kind: "static_resolver",
        administrator: false,
      },
    }),
  );
  await page.route("**/auth/session", (route) =>
    route.fulfill(
      route.request().headers().authorization === "Bearer scoped-token"
        ? {
            json: {
              authenticated: true,
              identity: {
                actor: "gateway-resolver",
                kind: "static_resolver",
                administrator: false,
              },
            },
          }
        : { json: { authenticated: false } },
    ),
  );
  await page.route("**/api/v2/repositories?**", (route) => {
    repositoryDiscoveryRequests += 1;
    return route.fulfill({ status: 403, json: { code: "access_denied" } });
  });

  await page.goto("/login");
  await page.getByText("Access Token", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Access Token" })
    .fill("scoped-token");
  await page.getByRole("button", { name: /Verify and sign in/ }).click();

  await expect(page).toHaveURL(/\/search$/);
  await expect(
    page.getByRole("heading", { name: "Global search" }),
  ).toBeVisible();
  expect(repositoryDiscoveryRequests).toBe(0);
});
