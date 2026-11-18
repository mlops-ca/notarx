/**
 * fr-CA writing rules checked mechanically (ADR 0014, "fr-CA writing rules").
 * Placeholders ({name}) and code spans (`email`) are data, not prose, and are
 * skipped.
 */
export function frCAStyleIssues(message: string): string[] {
  const prose = message.replace(/\{[^{}]+\}/g, "").replace(/`[^`]*`/g, "");
  const issues: string[] = [];
  if (/\be-?mails?\b/i.test(prose)) {
    issues.push("use courriel, not e-mail or email");
  }
  if (/ [:?!;]/.test(prose)) {
    issues.push(
      "no regular space before : ? ! ; (no space before ? ! ;, a no-break space before :)",
    );
  }
  if (/\p{L}'\p{L}/u.test(prose)) {
    issues.push("use the typographic apostrophe between letters");
  }
  if (/«(?!\u00a0)|(?<!\u00a0)»/.test(prose)) {
    issues.push("guillemets take a no-break space inside");
  }
  return issues;
}
