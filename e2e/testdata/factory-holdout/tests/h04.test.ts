// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #4: HEAD /:slug still answers 302 with Location but does not count as a click; only GET counts.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #4 HEAD is not a click (${CANARY})`, () => {
  it("HEAD answers 302 with the Location header", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/head" })).json();
    const res = await app.head(`/${slug}`);
    expect(res.status).toBe(302);
    expect(res.location).toBe("https://example.com/head");
  });

  it("HEAD does not count, GET does", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/count" })).json();
    for (let i = 0; i < 3; i++) await app.head(`/${slug}`);
    expect((await app.get(`/stats/${slug}`)).json().clicks).toBe(0);
    expect((await app.get(`/${slug}`)).status).toBe(302);
    expect((await app.get(`/stats/${slug}`)).json().clicks).toBe(1);
    await app.head(`/${slug}`);
    expect((await app.get(`/stats/${slug}`)).json().clicks).toBe(1);
  });
});
