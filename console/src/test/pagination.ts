import { screen, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

// Ant Design 6.7 puts the click handler on the control inside the page item,
// not on the titled list item, so every test clicks through this one helper.
export async function clickPage(user: UserEvent, page: number) {
  await user.click(within(screen.getByTitle(String(page))).getByRole("button"));
}
