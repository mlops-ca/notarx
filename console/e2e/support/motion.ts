import { expect, type Locator, type Page } from "@playwright/test";

/** AntD visibility starts before the opening scale has finished. */
export async function waitForModalOpen(dialog: Locator) {
  await expect(dialog).toBeVisible();
  await expect(dialog).not.toHaveClass(/ant-zoom-(?:appear|enter)/);
}

/**
 * Opens an AntD Select and returns its dropdown once the list is usable.
 * AntD mounts the list, then re-renders (and so replaces) its option nodes
 * when the dropdown motion starts, and visibility starts before the slide has
 * finished; a click on an option before then lands on a detached node. The
 * dropdown is found through the combobox's own listbox, so another Select's
 * closing list is never mistaken for it.
 */
export async function openSelect(page: Page, combobox: Locator) {
  await combobox.click();
  await expect(combobox).toHaveAttribute("aria-controls", /.+/);
  const listId = (await combobox.getAttribute("aria-controls")) ?? "";
  const dropdown = page
    .locator(".ant-select-dropdown")
    .filter({ has: page.locator(`[id="${listId}"]`) });
  await expect(dropdown).toBeVisible();
  // An async Select (the artifact picker) fetches its options; while it does,
  // the option nodes are replaced when the results land, so a click made
  // before then is lost. AntD marks the root `ant-select-loading` meanwhile.
  await expect(
    combobox.locator(
      "xpath=ancestor::div[contains(concat(' ', normalize-space(@class), ' '), ' ant-select ')][1]",
    ),
  ).not.toHaveClass(/ant-select-loading/);
  await expect(dropdown).not.toHaveClass(
    /ant-slide-(?:up|down)-(?:appear|enter)/,
  );
  await dropdown.evaluate(async (element) => {
    await Promise.all(
      element
        .getAnimations({ subtree: true })
        .filter((animation) =>
          Number.isFinite(animation.effect?.getComputedTiming().endTime ?? 0),
        )
        .map((animation) => animation.finished.catch(() => undefined)),
    );
  });
  return dropdown;
}

/**
 * Waits for every finite CSS animation and transition on the page (for
 * example the 220ms ag-feedback-enter entrance, which shifts a box by up to
 * 3px) to finish, so layout measurements read the settled geometry.
 */
export async function waitForAnimationsToSettle(page: Page) {
  await page.evaluate(async () => {
    const finite = () =>
      document
        .getAnimations()
        .filter((animation) =>
          Number.isFinite(animation.effect?.getComputedTiming().endTime ?? 0),
        );
    // Finishing an animation can start another, so loop (bounded) until none
    // is left. A cancelled animation rejects `finished`; it is settled too.
    for (let round = 0; round < 10; round++) {
      const pending = finite();
      if (pending.length === 0) return;
      await Promise.all(
        pending.map((animation) => animation.finished.catch(() => undefined)),
      );
    }
  });
}
