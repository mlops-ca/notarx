import { LocalizedError } from "../messages/translate";

type BlockingRule = { name?: unknown };

/**
 * Turns the 409 in_use problem of a remote-instance delete into a localized
 * message that names the promotion rules still targeting it. Any other error is
 * returned unchanged.
 */
export function remoteInstanceInUse(error: unknown, name: string): unknown {
  if (
    typeof error !== "object" ||
    error === null ||
    !("code" in error) ||
    error.code !== "in_use"
  ) {
    return error;
  }
  const rules =
    "rules" in error && Array.isArray(error.rules)
      ? (error.rules as BlockingRule[])
      : [];
  const named = rules.map((rule) => String(rule.name ?? "")).join(", ");
  return new LocalizedError(
    "remoteInstances.inUse",
    { name, rules: named || "-" },
    { status: 409 },
  );
}
