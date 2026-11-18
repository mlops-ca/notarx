import { screen } from "@testing-library/react";

// Ant Design 6.7 renders each allowClear input icon as a text-less button that
// shares its accessible name with a labelled action such as "Clear". Pick the
// button whose visible text carries the name.
export function getLabelledButton(name: RegExp): HTMLElement {
  const match = screen
    .getAllByRole("button", { name })
    .find((button) => name.test(button.textContent ?? ""));
  if (!match) {
    throw new Error(`no labelled button matches ${name}`);
  }
  return match;
}
