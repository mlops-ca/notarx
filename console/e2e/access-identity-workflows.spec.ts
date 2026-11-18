import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { waitForModalOpen } from "./support/motion";

/**
 * Write flows of the access and identity pages against a live Gateway. Every
 * step is confirmed through the management API, not only on screen, so a
 * refactor that renders the same text but writes the wrong thing fails here.
 */

const token = process.env.NOTARX_ADMIN_TOKEN;

test.skip(!token, "NOTARX_ADMIN_TOKEN is required for the access gate");
test.describe.configure({ mode: "serial" });

const suffix = `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
const repositoryName = `e2e-access-${suffix}`;
const grantAccountName = `e2e-grant-${suffix}`;
let repositoryId = "";
let grantAccountId = "";
let drawerUserId = "";

const admin = () => ({ Authorization: `Bearer ${token!}` });

async function expectOK(
  response: Awaited<ReturnType<APIRequestContext["get"]>>,
  action: string,
) {
  expect(
    response.ok(),
    `${action}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
}

async function authenticate(page: Page) {
  await page.addInitScript((accessToken) => {
    localStorage.setItem("ag.console.token", accessToken);
    localStorage.setItem("ag.console.role", "admin");
  }, token!);
}

interface GrantRecord {
  repositoryId: string;
  principal: string;
  scopes: string[];
}

async function grantsFor(request: APIRequestContext, principal: string) {
  const response = await request.get("/api/v2/repository-grants", {
    headers: admin(),
  });
  await expectOK(response, "list repository grants");
  return ((await response.json()) as GrantRecord[]).filter(
    (grant) =>
      grant.repositoryId === repositoryId && grant.principal === principal,
  );
}

async function identityStatus(request: APIRequestContext, bearer: string) {
  const response = await request.get("/api/v2/identity", {
    headers: { Authorization: `Bearer ${bearer}` },
  });
  return response.status();
}

test.beforeAll(async ({ request }) => {
  const repository = await request.post("/api/v2/repositories", {
    headers: { ...admin(), "Idempotency-Key": `e2e-access-repo-${suffix}` },
    data: { name: repositoryName, format: "raw", type: "hosted" },
  });
  await expectOK(repository, "create repository");
  repositoryId = (await repository.json()).id as string;

  const account = await request.post("/api/v2/service-accounts", {
    headers: { ...admin(), "Idempotency-Key": `e2e-access-sa-${suffix}` },
    data: { name: grantAccountName, description: "access E2E principal" },
  });
  await expectOK(account, "create service account");
  grantAccountId = (await account.json()).id as string;
});

test.afterAll(async ({ request }) => {
  if (repositoryId) {
    await request.delete(`/api/v2/repositories/${repositoryId}`, {
      headers: admin(),
    });
  }
  if (drawerUserId) {
    await request.delete(`/api/v2/users/${drawerUserId}`, {
      headers: admin(),
    });
  }
});

test("a repository grant is created, edited from Access Control, and removed", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const principal = `service-account:${grantAccountId}`;

  await page.goto(`/repositories/${repositoryId}?tab=grants`);
  await page
    .getByRole("button", { name: /Add grant/ })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Add grant" });
  await waitForModalOpen(dialog);
  await dialog.getByRole("combobox", { name: "Principal" }).click();
  await page.keyboard.type(grantAccountName);
  // Rich option labels: Ant Design names the accessible option by its value,
  // so pick the visible row that shows the account name.
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: grantAccountName })
    .click();
  await dialog.getByRole("combobox", { name: "Permission level" }).click();
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: "Read · browse / pull" })
    .click();
  await dialog.getByRole("button", { name: /^Save$/ }).click();
  await expect(dialog).toBeHidden();
  await expect
    .poll(async () => (await grantsFor(request, principal))[0]?.scopes)
    .toEqual(["repositories:read"]);

  // The cross-repository list shows the grant and leads back to edit it.
  await page.goto("/access");
  const row = page.getByRole("row").filter({ hasText: repositoryName });
  await expect(row).toBeVisible();
  await row.getByRole("link", { name: /Edit/ }).click();
  await expect(page).toHaveURL(new RegExp(`/repositories/${repositoryId}`));

  const grantRow = page.getByRole("row").filter({ hasText: grantAccountName });
  await grantRow.getByRole("button", { name: /^Edit$/ }).click();
  const editor = page.getByRole("dialog", { name: "Edit grant" });
  await waitForModalOpen(editor);
  await editor.getByRole("combobox", { name: "Permission level" }).click();
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: "Write · publish / edit" })
    .click();
  await editor.getByRole("button", { name: /^Save$/ }).click();
  await expect(editor).toBeHidden();
  await expect
    .poll(async () => (await grantsFor(request, principal))[0]?.scopes)
    .toContain("repositories:write");

  await grantRow.getByRole("button", { name: "Remove this grant row" }).click();
  await page
    .getByRole("button", { name: /^Remove$/ })
    .last()
    .click();
  await expect
    .poll(async () => (await grantsFor(request, principal)).length)
    .toBe(0);

  await page.goto("/access");
  await expect(
    page.getByRole("row").filter({ hasText: repositoryName }),
  ).toHaveCount(0);
});

test("a service account credential is issued, rotated, and revoked", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const accountName = `e2e-rotate-${suffix}`;

  await page.goto("/service-accounts");
  await page
    .getByRole("button", { name: /New service account/ })
    .first()
    .click();
  const create = page.getByRole("dialog", { name: "New service account" });
  await waitForModalOpen(create);
  await create.getByLabel("Account name").fill(accountName);
  await create.getByRole("button", { name: /^Create$/ }).click();
  await expect(create).toBeHidden();
  await expect(page.getByText(accountName).first()).toBeVisible();

  const issue = async (credentialName: string) => {
    await page.getByRole("button", { name: /Issue credential/ }).click();
    const dialog = page.getByRole("dialog", { name: "Issue credential" });
    await waitForModalOpen(dialog);
    await dialog.getByLabel("Credential name").fill(credentialName);
    await dialog.getByRole("button", { name: /^Issue$/ }).click();
    const reveal = page.getByRole("dialog", {
      name: "Credential issued: save the token",
    });
    const plaintext = (
      await reveal.locator(".break-all").first().textContent()
    )?.trim();
    expect(plaintext).toBeTruthy();
    await reveal.getByRole("button", { name: "I stored it securely" }).click();
    await expect(reveal).toBeHidden();
    return plaintext!;
  };

  const first = await issue(`first-${suffix}`);
  expect(await identityStatus(request, first)).toBe(200);

  // Rotation: a second credential works alongside the first, then the first
  // is revoked without interrupting the second.
  const second = await issue(`second-${suffix}`);
  expect(await identityStatus(request, second)).toBe(200);
  expect(await identityStatus(request, first)).toBe(200);

  await page
    .getByRole("row")
    .filter({ hasText: `first-${suffix}` })
    .getByRole("button", { name: /Revoke/ })
    .click();
  await page
    .getByRole("dialog", { name: "Revoke credential" })
    .getByRole("button", { name: /Revoke/ })
    .click();
  await expect.poll(() => identityStatus(request, first)).toBe(401);
  expect(await identityStatus(request, second)).toBe(200);
});

test("an API key is created, authenticates, and is revoked", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const keyName = `e2e-key-${suffix}`;

  await page.goto("/keys");
  await page
    .getByRole("button", { name: /New key/ })
    .first()
    .click();
  const create = page.getByRole("dialog", { name: "New API key" });
  await waitForModalOpen(create);
  await create.getByLabel("Key name").fill(keyName);
  await create.getByRole("button", { name: /^Create$/ }).click();
  const reveal = page.getByRole("dialog", {
    name: "API key created: save the token now",
  });
  const plaintext = (
    await reveal.locator(".break-all").first().textContent()
  )?.trim();
  expect(plaintext).toBeTruthy();
  await reveal.getByRole("button", { name: "I saved it" }).click();
  expect(await identityStatus(request, plaintext!)).toBe(200);

  await page.getByRole("button", { name: `Revoke key ${keyName}` }).click();
  await page
    .getByRole("dialog", { name: "Revoke API key" })
    .getByRole("button", { name: /^Revoke$/ })
    .click();
  await expect.poll(() => identityStatus(request, plaintext!)).toBe(401);
});

test("a custom role feeds a template that is applied to a repository", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const roleName = `e2e-role-${suffix}`;
  const templateName = `e2e-template-${suffix}`;
  const principal = `service-account:${grantAccountId}`;
  const named = async (path: string, name: string) => {
    const response = await request.get(path, { headers: admin() });
    await expectOK(response, `list ${path}`);
    return ((await response.json()) as Array<{ name: string }>).filter(
      (item) => item.name === name,
    );
  };

  await page.goto("/access?tab=policies");
  await page.getByRole("button", { name: /New role/ }).click();
  const roleDialog = page.getByRole("dialog", {
    name: "New authorization role",
  });
  await waitForModalOpen(roleDialog);
  await roleDialog.getByLabel("Role name").fill(roleName);
  await roleDialog.getByLabel("Permissions").click();
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: "Write" })
    .click();
  // A multiple select keeps its list open over the footer; close it first.
  await roleDialog.getByLabel("Role name").click();
  await expect(page.locator(".ant-select-dropdown:visible")).toHaveCount(0);
  await roleDialog.getByRole("button", { name: /^Save$/ }).click();
  await expect(roleDialog).toBeHidden();
  await expect
    .poll(
      async () => (await named("/api/v2/authorization-roles", roleName)).length,
    )
    .toBe(1);

  // The template editor reads the same roles list, so the new role is offered
  // without reloading the page.
  await page.getByRole("button", { name: /New template/ }).click();
  const editor = page.getByRole("dialog", {
    name: "New authorization template",
  });
  await waitForModalOpen(editor);
  await editor.getByPlaceholder("Template name").fill(templateName);
  await editor.getByRole("combobox").first().fill(principal);
  await editor.getByText("Custom permissions").click();
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: roleName })
    .click();
  await editor.getByRole("button", { name: /^Save$/ }).click();
  await expect(editor).toBeHidden();
  const templateRow = page.getByRole("row").filter({ hasText: templateName });
  await expect(templateRow).toBeVisible();

  await templateRow.getByRole("button", { name: /Apply/ }).click();
  const apply = page.getByRole("dialog", {
    name: "Apply authorization template",
  });
  await waitForModalOpen(apply);
  await apply.getByRole("combobox").click();
  await page.keyboard.type(repositoryName);
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: repositoryName })
    .click();
  await apply.getByRole("button", { name: /^Apply$/ }).click();
  await expect(apply).toBeHidden();
  await expect
    .poll(async () => (await grantsFor(request, principal))[0]?.scopes)
    .toEqual(
      expect.arrayContaining(["repositories:read", "repositories:write"]),
    );

  // Applying refreshes the cross-repository list on the grants tab.
  await page.getByRole("tab", { name: /Grants/ }).click();
  await expect(
    page.getByRole("row").filter({ hasText: repositoryName }),
  ).toBeVisible();

  await page.getByRole("tab", { name: /Policies & templates/ }).click();
  await templateRow.getByRole("button", { name: "Delete template" }).click();
  await page
    .getByRole("button", { name: /^Delete$/ })
    .last()
    .click();
  await expect(templateRow).toHaveCount(0);
  expect(
    await named("/api/v2/authorization-templates", templateName),
  ).toHaveLength(0);

  await page
    .getByRole("row")
    .filter({ hasText: roleName })
    .getByRole("button", { name: "Delete role" })
    .click();
  await page
    .getByRole("button", { name: /^Delete$/ })
    .last()
    .click();
  await expect(page.getByRole("row").filter({ hasText: roleName })).toHaveCount(
    0,
  );
  expect(await named("/api/v2/authorization-roles", roleName)).toHaveLength(0);
});

test("the user drawer saves a profile and adds, narrows, and removes a repository grant", async ({
  page,
  request,
}) => {
  await authenticate(page);
  const username = `e2e-drawer-${suffix}`;
  const principal = `user:${username}`;
  const created = await request.post("/api/v2/users", {
    headers: admin(),
    data: {
      name: username,
      displayName: "Drawer grant E2E",
      email: `${username}@example.test`,
      password: `Drawer-${suffix}`,
      role: "member",
    },
  });
  await expectOK(created, "create user");
  drawerUserId = ((await created.json()) as { id: string }).id;

  await page.goto("/users");
  await page.getByText(username, { exact: true }).click();
  const drawer = page
    .getByRole("dialog")
    .filter({ hasText: "Repository grant" });

  // A profile save shows in the list behind the drawer without a reload.
  const renamed = `Drawer renamed ${suffix}`;
  await drawer.getByLabel("Display name").fill(renamed);
  await drawer.getByRole("button", { name: /Save changes/ }).click();
  await expect(
    page.getByRole("row").filter({ hasText: username }),
  ).toContainText(renamed);
  const profile = await request.get(`/api/v2/users/${drawerUserId}`, {
    headers: admin(),
  });
  await expectOK(profile, "read user");
  expect(((await profile.json()) as { displayName: string }).displayName).toBe(
    renamed,
  );
  await drawer.getByRole("button", { name: /Add grant/ }).click();

  const editor = page.getByRole("dialog", { name: "Add repository grant" });
  await waitForModalOpen(editor);
  await editor.getByRole("combobox").first().click();
  await page.keyboard.type(repositoryName);
  await page
    .locator(".ant-select-item-option")
    .filter({ hasText: repositoryName })
    .click();
  await expect(
    editor.getByRole("combobox", { name: "Permission level" }),
  ).toBeVisible();
  await editor.getByRole("button", { name: /^Save$/ }).click();
  await expect(editor).toBeHidden();
  await expect
    .poll(async () => (await grantsFor(request, principal)).length)
    .toBe(1);
  const row = drawer.getByRole("listitem").filter({ hasText: repositoryName });
  await expect(row).toContainText("Entire repository");

  // Narrowing the scope moves the entry: the repository-wide row goes and
  // exactly one prefixed row remains.
  await row.getByRole("button", { name: "Edit this grant" }).click();
  const edit = page.getByRole("dialog", { name: "Edit repository grant" });
  await waitForModalOpen(edit);
  await edit.getByText("Limit by prefix", { exact: true }).click();
  // The prefix field is the dialog's last combobox (an AutoComplete).
  await edit.getByRole("combobox").last().fill("releases/");
  await edit.getByRole("button", { name: /^Save$/ }).click();
  await expect(edit).toBeHidden();
  await expect
    .poll(async () =>
      (await grantsFor(request, principal)).map(
        (grant) => (grant as { resourcePrefix?: string }).resourcePrefix,
      ),
    )
    .toEqual(["releases/"]);
  await expect(row).toContainText("releases/");

  await row.getByRole("button", { name: "Remove repository grant" }).click();
  // Move onto the open confirmation the way a pointer would, so the trigger's
  // tooltip closes instead of covering the confirm button.
  const confirmation = page.locator(".ant-popconfirm:visible");
  await confirmation.getByText("Remove this user's grant?").hover();
  await expect(
    page.getByRole("tooltip", { name: "Remove repository grant" }),
  ).toBeHidden();
  await confirmation.getByRole("button", { name: /^Remove$/ }).click();
  await expect
    .poll(async () => (await grantsFor(request, principal)).length)
    .toBe(0);
  await expect(row).toHaveCount(0);
});
