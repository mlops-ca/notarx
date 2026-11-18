import { defineConfig } from "@playwright/test";

const port = Number(process.env.PLAYWRIGHT_PORT ?? 4173);
const externalServer = process.env.PLAYWRIGHT_EXTERNAL_SERVER === "1";

export default defineConfig({
  testDir: "./e2e",
  // Visual baselines run fully mocked through playwright.visual.config.ts.
  testIgnore: "visual/**",
  timeout: 30_000,
  // CI shards write blob reports that the merge-reports job turns into one
  // HTML report; see docs/ci.md.
  reporter: process.env.PLAYWRIGHT_BLOB_REPORT
    ? [["dot"], ["blob"]]
    : process.env.CI
      ? [["dot"], ["html", { open: "never" }]]
      : undefined,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: "chromium",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: externalServer
    ? undefined
    : {
        command: `npm run dev -- --host 127.0.0.1 --port ${port}`,
        port,
        reuseExistingServer: false,
      },
});
