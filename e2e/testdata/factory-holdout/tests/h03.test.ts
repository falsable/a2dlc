// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #3: GET /stats/:slug for an unknown slug answers 404 with {"error": "not found"}, as the redirect route does.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #3 stats of an unknown slug (${CANARY})`, () => {
  it("answers 404 with {error: 'not found'}, the same body as the redirect route", async () => {
    const app = await start();
    const stats = await app.get("/stats/nosuchslug");
    expect(stats.status).toBe(404);
    expect(stats.json()).toEqual({ error: "not found" });
    const hop = await app.get("/nosuchslug");
    expect(stats.json()).toEqual(hop.json());
  });

  it("a known slug still answers 200 with its url and clicks", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/stats" })).json();
    await app.get(`/${slug}`);
    await app.get(`/${slug}`);
    const stats = await app.get(`/stats/${slug}`);
    expect(stats.status).toBe(200);
    expect(stats.json()).toMatchObject({ slug, url: "https://example.com/stats", clicks: 2 });
  });
});
