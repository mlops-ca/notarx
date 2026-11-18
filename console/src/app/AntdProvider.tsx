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

  // The antd and dayjs locale objects load on demand (ADR 0014), so the bundle
  // does not carry all three, like the message tables (`loadMessages`). The
  // Console switches language only once its messages have loaded; while the
  // antd locale loads, or when its chunk cannot be fetched (offline, stale
  // deploy), the previous antd locale stays: widgets keep working in the old
  // language rather than failing.
  useEffect(() => {
    let current = true;
    const info = localeInfo(locale);
    info
      .load()
      .then((loaded) => {
        if (!current) return;
        dayjs.locale(info.dayjs);
        setAntd(loaded);
      })
      .catch(() => undefined);
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
