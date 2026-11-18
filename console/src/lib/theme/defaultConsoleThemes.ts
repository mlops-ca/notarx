import type { ConsoleTheme } from "../../client";
import { productName } from "../brand";

// These values mirror the server-embedded Theme Packages and are only used
// while site settings are unavailable or come from an older Console build.
// Runtime behavior is always resolved by the semantic theme projection.
// Palette: the mlops.ca brand (ADR 0013). Signal-lime is a fill colour that
// always carries ink text; on light surfaces text accents use dark green.
export const defaultConsoleThemes: ConsoleTheme[] = [
  {
    schemaVersion: 1,
    id: "notarx-dark",
    name: `${productName} Dark`,
    description:
      "Default dark theme: ink surfaces, hairline borders and a signal-lime accent used for fills, links and focus.",
    mode: "dark",
    token: {
      colorPrimary: "#C8F135",
      colorSuccess: "#4ADE80",
      colorWarning: "#FBBF24",
      colorError: "#FF7A52",
      colorInfo: "#7DB7E8",
      colorTextBase: "#ECEBE3",
      colorBgBase: "#0B0C0A",
      colorText: "#ECEBE3",
      colorTextSecondary: "#9CA091",
      colorTextTertiary: "#8A8E7F",
      colorTextQuaternary: "#80846F",
      colorTextDisabled: "#5C6053",
      colorBgContainer: "#161812",
      colorBgElevated: "#1D2018",
      colorBgLayout: "#0B0C0A",
      colorBgSpotlight: "#2A2D24",
      colorBorder: "#2A2D24",
      colorBorderSecondary: "#20231B",
      colorFillAlter: "rgba(236, 235, 227, 0.03)",
      colorFillContent: "rgba(236, 235, 227, 0.06)",
      colorFillContentHover: "rgba(236, 235, 227, 0.09)",
      colorLink: "#C8F135",
      colorLinkHover: "#D9F76B",
      colorLinkActive: "#A9D11F",
      colorPrimaryHover: "#D6F55C",
      colorPrimaryActive: "#B4D92A",
      controlOutline: "rgba(200, 241, 53, 0.40)",
    },
  },
  {
    schemaVersion: 1,
    id: "notarx-light",
    name: `${productName} Light`,
    description:
      "Default light theme: paper surfaces, hairline borders and a signal-lime fill with a dark-green accent for text.",
    mode: "light",
    token: {
      colorPrimary: "#1D2018",
      colorSuccess: "#2E6B1F",
      colorWarning: "#8A5A00",
      colorError: "#B0310F",
      colorInfo: "#1F5F99",
      colorTextBase: "#12140E",
      colorBgBase: "#EDECE3",
      colorText: "#12140E",
      colorTextSecondary: "#555A4A",
      colorTextTertiary: "#5F6454",
      colorTextQuaternary: "#666B5A",
      colorTextDisabled: "#8A8E7F",
      colorBgContainer: "#F6F5EE",
      colorBgElevated: "#FBFAF4",
      colorBgLayout: "#EDECE3",
      colorBgSpotlight: "#12140E",
      colorBorder: "#CFCDBF",
      colorBorderSecondary: "#DDDBCE",
      colorFillAlter: "#F1F0E8",
      colorFillContent: "#E6E4D8",
      colorFillContentHover: "#DEDCCF",
      colorLink: "#3F5C00",
      colorLinkHover: "#2C4200",
      colorLinkActive: "#1F3000",
      colorPrimaryHover: "#2A2D24",
      colorPrimaryActive: "#3A3E32",
      controlOutline: "rgba(63, 92, 0, 0.35)",
    },
  },
];
