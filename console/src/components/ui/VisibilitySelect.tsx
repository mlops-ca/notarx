import { Select } from "antd";
import type { Visibility } from "../../client";
import { usePreferences } from "../../lib/preferences";

const ORDER: Visibility[] = ["private", "authenticated", "public"];

/** The localized name of a visibility tier (ADR 0029). */
export function useVisibilityLabel() {
  const { t } = usePreferences();
  return (visibility: Visibility) =>
    ({
      private: t("repository.visibilityPrivate"),
      authenticated: t("repository.visibilityAuthenticated"),
      public: t("repository.visibilityPublic"),
    })[visibility];
}

/** The three-option read visibility selector shared by repositories and groups. */
export function VisibilitySelect({
  value,
  onChange,
  ariaLabel,
  disabled,
}: {
  value: Visibility;
  onChange: (visibility: Visibility) => void;
  ariaLabel: string;
  disabled?: boolean;
}) {
  const label = useVisibilityLabel();
  return (
    <Select<Visibility>
      className="w-44"
      value={value}
      onChange={onChange}
      aria-label={ariaLabel}
      disabled={disabled}
      options={ORDER.map((tier) => ({ value: tier, label: label(tier) }))}
    />
  );
}
