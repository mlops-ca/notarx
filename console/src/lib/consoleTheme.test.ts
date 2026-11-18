import { afterEach, describe, expect, it } from "vitest";
import {
  applyConsoleTheme,
  buildConsoleThemeConfig,
  defaultConsoleThemes,
  resolveConsoleTheme,
} from "./consoleTheme";

afterEach(() => {
  document.documentElement.removeAttribute("style");
  delete document.documentElement.dataset.themeContract;
});

describe("console themes", () => {
  it("preserves explicit Ant Design aliases after the mode algorithm", () => {
    const dark = defaultConsoleThemes.find(
      (theme) => theme.id === "notarx-dark",
    )!;
    const { token } = resolveConsoleTheme(dark);

    expect(token.colorPrimary).toBe("#C8F135");
    expect(token.colorBgLayout).toBe("#0B0C0A");
    expect(token.colorBgContainer).toBe("#161812");
    expect(token.colorBgElevated).toBe("#1D2018");
    expect(token.colorBorder).toBe("#2A2D24");
    expect(token.colorTextSecondary).toBe("#9CA091");
  });

  it("derives Console CSS variables from the same resolved token map", () => {
    const light = defaultConsoleThemes.find(
      (theme) => theme.id === "notarx-light",
    )!;

    applyConsoleTheme(light, document.documentElement);

    expect(
      document.documentElement.style.getPropertyValue("--ag-action-primary"),
    ).toBe("#1d2018");
    expect(
      document.documentElement.style.getPropertyValue("--ag-surface-container"),
    ).toBe("#f6f5ee");
    expect(
      document.documentElement.style.getPropertyValue("--ag-status-danger"),
    ).toBe("#B0310F");
    expect(document.documentElement).toHaveAttribute(
      "data-theme-contract",
      "semantic-v1",
    );
  });

  it("projects the notarx Dark v2 shell palette", () => {
    const dark = defaultConsoleThemes.find(
      (theme) => theme.id === "notarx-dark",
    )!;

    applyConsoleTheme(dark, document.documentElement);

    expect(document.documentElement.style.getPropertyValue("--ag-sider")).toBe(
      "",
    );
    expect(
      document.documentElement.style.getPropertyValue("--ag-surface-sider"),
    ).toBe("#0e100c");
    expect(
      document.documentElement.style.getPropertyValue(
        "--ag-surface-container-translucent",
      ),
    ).toBe("rgba(22, 24, 18, 0.9)");
    expect(
      document.documentElement.style.getPropertyValue(
        "--ag-action-primary-soft",
      ),
    ).toBe("rgba(200, 241, 53, 0.12)");

    const config = buildConsoleThemeConfig(dark);
    expect(config.components?.Menu?.darkItemBg).toBe("transparent");
    expect(config.components?.Menu?.darkItemSelectedColor).toBe("#f6f5ee");
    expect(config.components?.Button?.defaultBg).toBe("#161812");
    expect(config.components?.Input?.activeShadow).toBe(
      "0 0 0 2px rgba(200, 241, 53, 0.4)",
    );
    expect(config.components?.Segmented?.trackBg).toBe(
      "rgba(236, 235, 227, 0.04)",
    );
    expect(config.components?.Table?.headerColor).toBe("#8a8e7f");
  });

  it("keeps the notarx Light v2 menu on the sider surface", () => {
    const light = defaultConsoleThemes.find(
      (theme) => theme.id === "notarx-light",
    )!;
    const resolved = resolveConsoleTheme(light);

    expect(resolved.roles.surface.menu).toBe("transparent");
    expect(resolved.antDesign.components?.Menu?.itemBg).toBe("transparent");
    expect(resolved.antDesign.components?.Menu?.subMenuItemBg).toBe(
      "transparent",
    );
  });

  it.each(["success", "warning", "danger", "info"] as const)(
    "keeps notarx Light %s text above AA on resting and hovered surfaces",
    (status) => {
      const light = defaultConsoleThemes.find(
        (theme) => theme.id === "notarx-light",
      )!;
      const { roles } = resolveConsoleTheme(light);
      const luminance = (hex: string) => {
        expect(hex).toMatch(/^#[\da-f]{6}$/i);
        const channels = [1, 3, 5].map((offset) => {
          const value =
            Number.parseInt(hex.slice(offset, offset + 2), 16) / 255;
          return value <= 0.04045
            ? value / 12.92
            : ((value + 0.055) / 1.055) ** 2.4;
        });
        return (
          channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
        );
      };
      const foreground = luminance(roles.status[status].foreground);
      for (const surface of ["canvas", "container", "hover"] as const) {
        const background = luminance(roles.surface[surface]);
        const contrast =
          (Math.max(foreground, background) + 0.05) /
          (Math.min(foreground, background) + 0.05);
        expect(contrast, `${status} text on ${surface}`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    },
  );

  it("keeps extension menus on their package palette", () => {
    const extension = {
      ...defaultConsoleThemes[0],
      id: "operator-plum",
      name: "Operator Plum",
      token: {
        ...defaultConsoleThemes[0].token,
        colorPrimary: "#7C3AED",
        colorPrimaryHover: "#8B5CF6",
        colorPrimaryBg: "#1E1B4B",
      },
    };
    const config = buildConsoleThemeConfig(extension);

    expect(config.components?.Menu?.darkItemBg).toBe("transparent");
    expect(config.components?.Menu?.darkItemHoverBg).toBe(
      "rgba(236, 235, 227, 0.09)",
    );
    expect(config.components?.Menu?.darkItemSelectedBg).toBe("#1E1B4B");
    expect(config.components?.Menu?.darkItemSelectedColor).toBe("#8B5CF6");
  });

  it("keeps component geometry in the shared Console contract", () => {
    const config = buildConsoleThemeConfig(defaultConsoleThemes[0]);

    expect(config.components?.Menu?.itemHeight).toBe(38);
    expect(config.components?.Button?.borderRadius).toBe(2);
  });

  it("keeps native text selection neutral across every built-in theme", () => {
    for (const theme of defaultConsoleThemes) {
      const { roles } = resolveConsoleTheme(theme);

      const selection = roles.selection.background.toLowerCase();
      expect(selection).toContain(roles.content.primary.toLowerCase());
      expect(selection).toContain(roles.surface.container.toLowerCase());
      expect(selection).not.toContain(roles.action.primary.toLowerCase());
      expect(roles.selection.foreground.toLowerCase()).toBe(
        roles.content.primary.toLowerCase(),
      );
    }
  });

  it("projects the same complete semantic variable contract for every theme", () => {
    const variableSets = defaultConsoleThemes.map((theme) =>
      Object.keys(resolveConsoleTheme(theme).cssVariables).sort(),
    );

    expect(variableSets[0].length).toBeGreaterThan(50);
    for (const variables of variableSets.slice(1)) {
      expect(variables).toEqual(variableSets[0]);
    }
    expect(variableSets[0]).not.toContain("--ag-brand");
    expect(variableSets[0]).toContain("--ag-action-primary");
    expect(variableSets[0]).toContain("--ag-selection-background");
    expect(variableSets[0]).toContain("--ag-visualization-trend-primary");
  });

  it("keeps visualization colors independent from action and status meaning", () => {
    for (const theme of defaultConsoleThemes) {
      const { roles } = resolveConsoleTheme(theme);
      const operationalColors = [
        roles.action.primary,
        roles.status.success.foreground,
        roles.status.warning.foreground,
        roles.status.danger.foreground,
        roles.status.info.foreground,
      ].map((color) => color.toLowerCase());

      for (const color of roles.visualization.categorical) {
        expect(operationalColors).not.toContain(color.toLowerCase());
      }
      expect(roles.visualization.trendPrimary).not.toBe(roles.action.primary);
    }
  });

  it("resolves a v1 extension package without built-in CSS knowledge", () => {
    const extension = {
      ...defaultConsoleThemes[1],
      id: "operator-plum",
      name: "Operator Plum",
      token: {
        ...defaultConsoleThemes[1].token,
        colorPrimary: "#7C3AED",
        colorPrimaryHover: "#8B5CF6",
        colorPrimaryActive: "#6D28D9",
      },
    };

    const resolved = resolveConsoleTheme(extension);

    expect(resolved.roles.action.primary).toBe("#7C3AED");
    expect(resolved.roles.navigation.indicatorStart).toBe("#8B5CF6");
    expect(resolved.roles.surface.container).toBe("#F6F5EE");
    expect(resolved.roles.selection.background).not.toContain("#7C3AED");
    expect(Object.keys(resolved.cssVariables).length).toBeGreaterThan(50);
  });

  it.each([
    "#1234",
    "#12345678",
    "rgb(12, 34, 56)",
    "rgb(12%, 34%, 56%)",
    "rgba(12, 34, 56, 0.5)",
    "hsl(240, 100%, 50%)",
    "hsla(240, 100%, 50%, 25%)",
  ])(
    "derives stable semantic colors from the server color subset: %s",
    (colorPrimary) => {
      const base = defaultConsoleThemes[0];
      const resolved = resolveConsoleTheme({
        schemaVersion: 1,
        id: "server-color-contract",
        name: "Server Color Contract",
        mode: "dark",
        token: {
          colorPrimary,
          colorSuccess: base.token.colorSuccess,
          colorWarning: base.token.colorWarning,
          colorError: base.token.colorError,
          colorInfo: base.token.colorInfo,
          colorTextBase: base.token.colorTextBase,
          colorBgBase: base.token.colorBgBase,
        },
      });

      expect(resolved.roles.action.primary).toBe(colorPrimary);
      expect(resolved.roles.action.hover).not.toBe("#0e0e0e");
      expect(resolved.roles.action.active).not.toBe("#070707");
      expect(Object.values(resolved.cssVariables)).not.toContain("");
    },
  );

  it("removes obsolete generic variables when applying the semantic contract", () => {
    document.documentElement.style.setProperty("--ag-brand", "hotpink");
    document.documentElement.style.setProperty("--ag-text", "hotpink");

    applyConsoleTheme(defaultConsoleThemes[0], document.documentElement);

    expect(document.documentElement.style.getPropertyValue("--ag-brand")).toBe(
      "",
    );
    expect(document.documentElement.style.getPropertyValue("--ag-text")).toBe(
      "",
    );
  });
});

it.each([
  ["notarx-dark", "#0b0c0a", "#c8f135"],
  ["notarx-light", "#f6f5ee", "#1d2018"],
])(
  "projects readable %s action tokens without replacing global solid text",
  (id, foreground, background) => {
    const theme = defaultConsoleThemes.find((entry) => entry.id === id)!;
    const resolved = resolveConsoleTheme(theme);
    expect(resolved.roles.content.onAction).toBe(foreground);
    expect(resolved.roles.action.primary).toBe(background);
    expect(resolved.antDesign.components?.Button?.primaryColor).toBe(
      foreground,
    );
    expect(resolved.antDesign.components?.Button?.colorPrimary).toBe(
      background,
    );
    expect(resolved.antDesign.components?.Button?.colorPrimaryHover).toBe(
      resolved.roles.action.hover,
    );
    expect(resolved.antDesign.components?.Button?.colorPrimaryActive).toBe(
      resolved.roles.action.active,
    );
    expect(resolved.token.colorTextLightSolid).toBe("#fff");
  },
);
