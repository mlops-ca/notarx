import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { APTAssetDetail } from "./APTAssetDetail";

afterEach(cleanup);

describe("APTAssetDetail", () => {
  it("presents a proxy cache object with an exact upstream and download path", async () => {
    const user = userEvent.setup();
    const path = "pool/main/h/hello/hello_2.10_amd64.deb";

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <APTAssetDetail
            repoName="debian"
            meta={{
              coordinate: path,
              digest:
                "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
              size: 2048,
              contentType: "application/vnd.debian.binary-package",
              createdAt: "2026-08-11T04:30:00Z",
              cachedAt: "2026-08-11T05:30:00Z",
              sourceUrl: `https://deb.example.test/debian/${path}`,
            }}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(screen.getByText("First cached")).toBeInTheDocument();
    expect(screen.getByText("Package object")).toBeInTheDocument();
    expect(screen.getByText("Last cached")).toBeInTheDocument();
    expect(
      screen.getByText("application/vnd.debian.binary-package"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /hello_2.10_amd64.deb/ }),
    ).toHaveAttribute("href", `https://deb.example.test/debian/${path}`);

    await user.click(screen.getByText("Usage"));
    expect(
      await screen.findByText(
        `curl -fsSL ${window.location.origin}/apt/debian/${path} -o package`,
      ),
    ).toBeInTheDocument();
  });
});
