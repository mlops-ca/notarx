import { expect, test } from "@playwright/test";

const token = process.env.NOTARX_ADMIN_TOKEN;
const authorization = { Authorization: `Bearer ${token!}` };
let createdUserId = "";

test.skip(!token, "NOTARX_ADMIN_TOKEN is required for user governance E2E");

test.afterEach(async ({ request }) => {
  if (!createdUserId) return;
  const deleted = await request.delete(`/api/v2/users/${createdUserId}`, {
    headers: authorization,
  });
  expect(
    deleted.ok() || deleted.status() === 404,
    `delete user: ${deleted.status()} ${await deleted.text()}`,
  ).toBeTruthy();
  createdUserId = "";
});

test("mandatory password change keeps the restricted session in memory", async ({
  page,
  request,
}) => {
  const suffix = `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
  const username = `e2e-password-${suffix}`;
  const temporaryPassword = `Temporary-${suffix}`;
  const personalPassword = `Personal-${suffix}`;

  const created = await request.post("/api/v2/users", {
    headers: authorization,
    data: {
      name: username,
      displayName: "Password change E2E",
      email: `${username}@example.test`,
      description: "Temporary browser test account",
      password: temporaryPassword,
      role: "member",
      mustChangePassword: true,
    },
  });
  expect(
    created.ok(),
    `create user: ${created.status()} ${await created.text()}`,
  ).toBeTruthy();
  createdUserId = ((await created.json()) as { id: string }).id;

  const oidcConfig = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/auth/idp",
  );
  await page.goto("/login");
  const config = (await (await oidcConfig).json()) as { items?: unknown[] };
  if ((config.items?.length ?? 0) > 0) {
    await expect(
      page.getByText("Enterprise SSO", { exact: true }),
    ).toBeVisible();
  }
  const passwordMode = page.getByRole("radio", {
    name: /Username & Password/,
  });
  await page.getByText("Username & Password", { exact: true }).click();
  await expect(passwordMode).toBeChecked();
  await page.getByLabel("Username", { exact: true }).fill(username);
  const passwordInput = page.getByLabel("Password", { exact: true });
  await passwordInput.fill(temporaryPassword);

  const [loginResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/auth/login") &&
        response.request().method() === "POST",
      { timeout: 10_000 },
    ),
    passwordInput.press("Enter"),
  ]);
  expect(loginResponse.status()).toBe(200);
  const restricted = (await loginResponse.json()) as {
    token: string;
    mustChangePassword: boolean;
  };
  expect(restricted.mustChangePassword).toBe(true);

  await expect(
    page.getByText("Change initial password", { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => localStorage.getItem("ag.console.token")),
  ).toBeNull();

  await page.getByLabel("New password", { exact: true }).fill(personalPassword);
  const confirmPassword = page.getByLabel("Confirm new password", {
    exact: true,
  });
  await confirmPassword.fill(personalPassword);
  const [passwordResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/auth/change-password") &&
        response.request().method() === "POST",
      { timeout: 10_000 },
    ),
    confirmPassword.press("Enter"),
  ]);
  expect(passwordResponse.status()).toBe(204);

  await expect(page).toHaveURL("/repositories");
  const storedToken = await page.evaluate(() =>
    localStorage.getItem("ag.console.token"),
  );
  expect(storedToken).toBeTruthy();
  expect(storedToken).not.toBe(restricted.token);
});
