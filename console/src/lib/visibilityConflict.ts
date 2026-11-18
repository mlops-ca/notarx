import { LocalizedError } from "../messages/translate";

type ConflictMember = { name?: unknown; visibility?: unknown };

/**
 * Turns the 409 visibility_conflict problem (ADR 0029, strict group mode) into
 * a localized message that names the offending repositories. Any other error is
 * returned unchanged.
 */
export function visibilityConflict(error: unknown): unknown {
  if (
    typeof error !== "object" ||
    error === null ||
    !("code" in error) ||
    error.code !== "visibility_conflict"
  ) {
    return error;
  }
  const members =
    "members" in error && Array.isArray(error.members)
      ? (error.members as ConflictMember[])
      : [];
  const named = members
    .map(
      (member) =>
        `${String(member.name ?? "")} (${String(member.visibility ?? "")})`,
    )
    .join(", ");
  return new LocalizedError(
    "groups.visibilityConflict",
    { members: named || "-" },
    { status: 409 },
  );
}
