import { App as AntdApp, ConfigProvider } from "antd";
import dayjs from "dayjs";
import { useEffect, useState, type ReactNode } from "react";
import { usePreferences } from "../lib/preferences";
import { antdEnCA, localeInfo, type AntdLocale } from "../messages/locales";

export function AntdProvider({ children }: { children: ReactNode }) {
  const { locale, resolvedTheme } = usePreferences();
  const [antd, setAntd] = useState<AntdLocale | undefined>(() =>
    locale === "en-CA" ? antdEnCA : undefined,
  );

  // The locale objects load on demand (ADR 0014): the bundle carries only the
  // reference locale until a reader picks another. The previous language stays
  // on screen for the moment the next one loads.
  useEffect(() => {
    let current = true;
    const info = localeInfo(locale);
    void info.load().then((loaded) => {
      if (!current) return;
      dayjs.locale(info.dayjs);
      setAntd(loaded);
    });
    return () => {
      current = false;
    };
  }, [locale]);

  return (
    <ConfigProvider
      // Labels of an unspaced script keep their natural spacing (DESIGN.md): no
      // space is inserted between the two characters of a two-character label.
      button={{ autoInsertSpace: false }}
      componentSize="medium"
      locale={antd}
      theme={resolvedTheme.antDesign}
      variant="outlined"
    >
      <AntdApp className="min-h-screen">{children}</AntdApp>
    </ConfigProvider>
  );
}
