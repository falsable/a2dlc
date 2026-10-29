// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #2: only http and https are accepted; scheme-less input is rejected with 400 or normalized to https://;
// any other scheme is rejected with 400.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #2 only http and https (${CANARY})`, () => {
  it("accepts http and https URLs and redirects to them as given", async () => {
    const app = await start();
    for (const url of ["http://example.com/path?q=1", "https://example.org/docs/x"]) {
      const created = await app.post({ url });
      expect(created.status).toBe(201);
      const hop = await app.get(`/${created.json().slug}`);
      expect(hop.status).toBe(302);
      expect(hop.location).toBe(url);
    }
  });

  it("a URL without a scheme is rejected with 400 or redirects to an absolute https URL", async () => {
    const app = await start();
    const created = await app.post({ url: "example.com/landing" });
    if (created.status === 400) return;
    expect(created.status).toBe(201);
    const hop = await app.get(`/${created.json().slug}`);
    expect(hop.status).toBe(302);
    const target = new URL(hop.location ?? "");
    expect(hop.location?.startsWith("https://")).toBe(true);
    expect(target.hostname).toBe("example.com");
    expect(target.pathname).toBe("/landing");
  });

  it("rejects every other scheme with 400", async () => {
    const app = await start();
    const others = [
      "javascript:alert(1)",
      "JavaScript:alert(document.cookie)",
      "javascript://example.com/%0Aalert(1)",
      "ftp://example.com/file.txt",
      "data:text/html,<b>hi</b>",
      "file:///etc/passwd",
      "mailto:someone@example.com",
    ];
    for (const url of others) {
      const res = await app.post({ url });
      expect(res.status, url).toBe(400);
    }
  });
});
