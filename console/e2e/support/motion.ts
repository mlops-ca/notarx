import { expect, type Locator, type Page } from "@playwright/test";

/** AntD visibility starts before the opening scale has finished. */
export async function waitForModalOpen(dialog: Locator) {
  await expect(dialog).toBeVisible();
  await expect(dialog).not.toHaveClass(/ant-zoom-(?:appear|enter)/);
}

/**
 * AntD mounts an opened Select list, then re-renders (and so replaces) its
 * option nodes when the dropdown motion starts, and visibility starts before
 * the slide has finished. Clicking an option before that replacement lands the
 * click on a detached node. Wait for the list to be open with its motion
 * finished.
 */
export async function waitForSelectDropdownOpen(page: Page) {
  const dropdown = page
    .locator(".ant-select-dropdown:not(.ant-select-dropdown-hidden)")
    .last();
  await expect(dropdown).toBeVisible();
  await expect(dropdown).not.toHaveClass(/ant-slide-up-(?:appear|enter)/);
  await waitForAnimationsToSettle(page);
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
