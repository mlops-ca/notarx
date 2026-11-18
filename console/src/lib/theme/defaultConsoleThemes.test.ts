import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { defaultConsoleThemes } from "./defaultConsoleThemes";
import { resolveConsoleTheme } from "./semanticTheme";

type Rgba = [number, number, number, number];

function parse(color: string): Rgba {
  const hex = /^#([\da-f]{6})$/i.exec(color);
  if (hex) {
    const n = Number.parseInt(hex[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1];
  }
  const rgba = /^rgba?\(([^)]+)\)$/i.exec(color);
  if (!rgba) throw new Error(`unsupported colour ${color}`);
  const [r, g, b, a = "1"] = rgba[1].split(",").map((part) => part.trim());
  return [Number(r), Number(g), Number(b), Number(a)];
}

function over(top: Rgba, bottom: Rgba): Rgba {
  return [0, 1, 2]
    .map((i) => top[i] * top[3] + bottom[i] * (1 - top[3]))
    .concat(1) as Rgba;
}

function luminance([r, g, b]: Rgba) {
  const [lr, lg, lb] = [r, g, b].map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb;
}

function contrast(foreground: string, background: string, base: Rgba) {
  const bg = over(parse(background), base);
  const fg = over(parse(foreground), bg);
  const [high, low] = [luminance(fg), luminance(bg)].sort((a, b) => b - a);
  return (high + 0.05) / (low + 0.05);
}

describe("notarx default themes", () => {
  it("registers notarx-dark and notarx-light only", () => {
    expect(defaultConsoleThemes.map((theme) => theme.id)).toEqual([
      "notarx-dark",
      "notarx-light",
    ]);
  });

  it.each(defaultConsoleThemes.map((theme) => [theme.id, theme] as const))(
    "%s meets WCAG AA for every text and fill role pair",
    (_id, theme) => {
      const { roles } = resolveConsoleTheme(theme);
      const canvas = parse(roles.surface.canvas);
      const surfaces = {
        canvas: roles.surface.canvas,
        container: roles.surface.container,
        elevated: roles.surface.elevated,
        hover: roles.surface.hover,
      };
      const text = {
        "content.strong": roles.content.strong,
        "content.primary": roles.content.primary,
        "content.secondary": roles.content.secondary,
        "content.tertiary": roles.content.tertiary,
        "link.default": roles.link.default,
        "navigation.selectedText": roles.navigation.selectedText,
        "status.success": roles.status.success.foreground,
        "status.warning": roles.status.warning.foreground,
        "status.danger": roles.status.danger.foreground,
        "status.info": roles.status.info.foreground,
      };
      for (const [role, color] of Object.entries(text)) {
        for (const [name, surface] of Object.entries(surfaces)) {
          expect(
            contrast(color, surface, canvas),
            `${role} on ${name}`,
          ).toBeGreaterThanOrEqual(4.5);
        }
      }
      const fills = {
        "onAction on action.primary": [
          roles.content.onAction,
          roles.action.primary,
        ],
        "onAction on action.hover": [
          roles.content.onAction,
          roles.action.hover,
        ],
        "onAction on action.active": [
          roles.content.onAction,
          roles.action.active,
        ],
        "onIdentity on identity.gradientStart": [
          roles.content.onIdentity,
          roles.identity.gradientStart,
        ],
      };
      for (const [name, [fg, bg]] of Object.entries(fills)) {
        expect(contrast(fg, bg, canvas), name).toBeGreaterThanOrEqual(4.5);
      }
    },
  );
});

describe("default console themes", () => {
  it.each(defaultConsoleThemes.map((theme) => [theme.id, theme] as const))(
    "%s mirrors the server-embedded Theme Package",
    (id, theme) => {
      const embedded = JSON.parse(
        readFileSync(
          // Vitest runs from console/; the packages ship with the Gateway.
          resolve(`../internal/consoletheme/builtin/${id}.theme.json`),
          "utf8",
        ),
      );
      expect(theme).toEqual(embedded);
    },
  );
});
