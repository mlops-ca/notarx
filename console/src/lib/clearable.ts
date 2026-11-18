import type { Translate } from "./preferences";

// Ant Design renders every allowClear icon as a button named "Clear". Several
// clearable fields on one page then share an identical accessible name, so a
// screen reader user cannot tell them apart. Name each one after its field.
// The word comes from the existing access.clear message, which every locale carries.
export function clearable(t: Translate, field: string) {
  return { label: `${t("access.clear")}: ${field}` };
}
