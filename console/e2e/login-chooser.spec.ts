import { expect, test, type Page } from "@playwright/test";

// The sign-in page offers one button per enabled identity provider, taken from
// GET /auth/idp, and each button starts that provider's own login flow.
const providers = [
  { slug: "entra", displayName: "Entra ID", kind: "oidc" },
  { slug: "okta-saml", displayName: "Okta SAML", kind: "saml" },
];

async function mockProviders(page: Page, items: typeof providers) {
  await page.route("**/auth/idp", (route) =>
    route.fulfill({ json: { items } }),
  );
}

test.describe("Login chooser", () => {
  test("shows a button for each enabled provider and starts its flow", async ({
    page,
  }) => {
    await mockProviders(page, providers);
    const started: string[] = [];
    await page.route(/\/auth\/(oidc|saml)\/[^/]+\/login/, (route) => {
      started.push(new URL(route.request().url()).pathname);
      return route.fulfill({
        contentType: "text/html",
        body: "<p>identity provider</p>",
      });
    });

    await page.goto("/login?redirect=%2Frepositories");
    for (const provider of providers) {
      await expect(
        page.getByRole("button", { name: provider.displayName }),
      ).toBeVisible();
    }

    await page.getByRole("button", { name: "Okta SAML" }).click();
    await expect(page).toHaveURL(
      /\/auth\/saml\/okta-saml\/login\?redirect=%2Frepositories$/,
    );
    expect(started).toEqual(["/auth/saml/okta-saml/login"]);

    await page.goto("/login?redirect=%2F");
    await page.getByRole("button", { name: "Entra ID" }).click();
    await expect(page).toHaveURL(/\/auth\/oidc\/entra\/login\?redirect=%2F$/);
  });

  test("offers only password and token sign-in when no provider is enabled", async ({
    page,
  }) => {
    await mockProviders(page, []);
    await page.goto("/login");
    await expect(
      page.getByRole("radio", { name: /Username & Password/ }),
    ).toBeChecked();
    await expect(
      page.getByRole("radio", { name: /Enterprise SSO/ }),
    ).toHaveCount(0);
  });
});
