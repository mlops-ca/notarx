import type { RoleOverrides } from "./semanticThemeTypes";

export const mix = (
  color: string,
  percentage: number,
  background = "transparent",
) => `color-mix(in srgb, ${color} ${percentage}%, ${background})`;

// notarx brand roles (ADR 0013): ink and paper surfaces carry hierarchy with
// 1px hairlines and no structural shadows, signal-lime is a fill that carries ink
// text (dark theme actions), and links and focus use lime on dark and dark
// green on light.
export const builtInRoleOverrides: Readonly<Record<string, RoleOverrides>> = {
  "notarx-dark": {
    surface: {
      canvas: "#0b0c0a",
      canvasGradientStart: "#0b0c0a",
      canvasGradientEnd: "#0b0c0a",
      container: "#161812",
      containerTranslucent: "rgba(22, 24, 18, 0.9)",
      hover: "rgba(236, 235, 227, 0.05)",
      elevated: "#1d2018",
      disabled: "rgba(236, 235, 227, 0.04)",
      sider: "#0e100c",
      menu: "transparent",
      topbar: "rgba(11, 12, 10, 0.86)",
      tableHeader: "transparent",
      tableHover: "rgba(236, 235, 227, 0.035)",
    },
    content: {
      strong: "#f6f5ee",
      primary: "#ecebe3",
      secondary: "#9ca091",
      tertiary: "#8a8e7f",
      disabled: "#80846f",
      onAction: "#0b0c0a",
      onIdentity: "#0b0c0a",
    },
    border: {
      default: "#2a2d24",
      subtle: "#20231b",
      row: "#1b1d16",
      strong: "#3a3e32",
    },
    action: {
      // Signal-lime fill; text on it is always ink (content.onAction).
      primary: "#c8f135",
      hover: "#d6f55c",
      active: "#b4d92a",
      soft: "rgba(200, 241, 53, 0.12)",
      shadow: "none",
    },
    link: {
      default: "#c8f135",
      hover: "#d9f76b",
      active: "#a9d11f",
    },
    focus: { ring: "rgba(200, 241, 53, 0.4)" },
    navigation: {
      indicatorStart: "#c8f135",
      indicatorEnd: "#c8f135",
      selectedBackgroundStart: "rgba(236, 235, 227, 0.06)",
      selectedBackgroundEnd: "rgba(236, 235, 227, 0.06)",
      selectedText: "#f6f5ee",
    },
    identity: {
      gradientStart: "#c8f135",
      gradientMiddle: "#c8f135",
      gradientEnd: "#c8f135",
      outline: "rgba(236, 235, 227, 0.12)",
      outlineHover: "rgba(236, 235, 227, 0.24)",
      glow: "rgba(200, 241, 53, 0)",
      glowHover: "rgba(200, 241, 53, 0.1)",
    },
    effect: {
      // Hairlines carry structure; shadows are for overlays only.
      shadowStructural: "none",
      shadowElevated:
        "0 16px 48px -12px rgba(0, 0, 0, 0.6), 0 0 0 1px rgba(236, 235, 227, 0.08)",
      scrollbar: "#2a2d24",
      scrollbarHover: "#3a3e32",
    },
  },
  "notarx-light": {
    surface: {
      canvas: "#edece3",
      canvasGradientStart: "#edece3",
      canvasGradientEnd: "#edece3",
      container: "#f6f5ee",
      containerTranslucent: "rgba(246, 245, 238, 0.9)",
      hover: "#e8e7dc",
      elevated: "#fbfaf4",
      disabled: "#e8e7dc",
      sider: "#f1f0e8",
      menu: "transparent",
      topbar: "rgba(237, 236, 227, 0.86)",
      tableHeader: "transparent",
      tableHover: "#f1f0e8",
    },
    content: {
      strong: "#12140e",
      primary: "#12140e",
      secondary: "#555a4a",
      tertiary: "#5f6454",
      disabled: "#8a8e7f",
      onAction: "#f6f5ee",
      onIdentity: "#0b0c0a",
    },
    border: {
      default: "#cfcdbf",
      subtle: "#dddbce",
      row: "#e6e4d8",
      strong: "#b3b1a1",
    },
    action: {
      // Light surfaces take an ink fill: a lime fill would sit at about 1.2:1
      // against paper, below the 3:1 boundary contrast for controls.
      primary: "#1d2018",
      hover: "#2a2d24",
      active: "#3a3e32",
      soft: "rgba(18, 20, 14, 0.08)",
      shadow: "none",
    },
    link: {
      default: "#3f5c00",
      hover: "#2c4200",
      active: "#1f3000",
    },
    focus: { ring: "rgba(63, 92, 0, 0.35)" },
    navigation: {
      indicatorStart: "#3f5c00",
      indicatorEnd: "#3f5c00",
      selectedBackgroundStart: "rgba(18, 20, 14, 0.06)",
      selectedBackgroundEnd: "rgba(18, 20, 14, 0.06)",
      selectedText: "#12140e",
    },
    identity: {
      gradientStart: "#c8f135",
      gradientMiddle: "#c8f135",
      gradientEnd: "#c8f135",
      outline: "rgba(18, 20, 14, 0.14)",
      outlineHover: "rgba(18, 20, 14, 0.28)",
      glow: "rgba(200, 241, 53, 0)",
      glowHover: "rgba(200, 241, 53, 0.1)",
    },
    effect: {
      // Hairlines carry structure; shadows are for overlays only.
      shadowStructural: "none",
      shadowElevated:
        "0 16px 40px -12px rgba(18, 20, 14, 0.2), 0 0 0 1px rgba(18, 20, 14, 0.08)",
      scrollbar: "#cfcdbf",
      scrollbarHover: "#b3b1a1",
    },
  },
};
