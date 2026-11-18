import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { FormatProfile } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { OCIDiscoveryNotice } from "./OCIDiscoveryNotice";

const profiles = vi.hoisted(() => vi.fn());
vi.mock("../../features/repository/repositoryQueries", () => ({
  useFormatProfiles: profiles,
}));

const profile: FormatProfile = {
  format: "oci",
  repositoryTypes: ["hosted", "proxy"],
  groupSupported: true,
  publicVisibility: true,
  hostedOperations: ["read", "publish"],
  proxyOperations: ["read"],
  ociDiscovery: {
    catalog: true,
    referrers: true,
    tagFallback: false,
    signatureVerification: false,
  },
};

afterEach(() => {
  cleanup();
  profiles.mockReset();
});

it("explains cache coverage, source authority, and signer trust from advertised API support", () => {
  profiles.mockReturnValue({ data: [profile] });
  render(
    <PreferencesProvider>
      <OCIDiscoveryNotice />
    </PreferencesProvider>,
  );
  const notice = screen.getByRole("region", {
    name: "OCI catalog and attached artifacts",
  });
  expect(notice).toHaveTextContent(
    "The cache below is not a complete inventory",
  );
  expect(notice).toHaveTextContent("even after Group members are reordered");
  expect(notice).toHaveTextContent(
    "does not verify signer identity or trust policy",
  );
  expect(notice).toHaveTextContent("tag fallback is unavailable");
});

it.each([undefined, [], [{ ...profile, ociDiscovery: undefined }]])(
  "does not invent discovery capabilities while the server profile is absent: %j",
  (data) => {
    profiles.mockReturnValue({ data });
    const { container } = render(
      <PreferencesProvider>
        <OCIDiscoveryNotice />
      </PreferencesProvider>,
    );
    expect(container).toBeEmptyDOMElement();
  },
);
