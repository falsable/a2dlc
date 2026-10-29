// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #6: with expiresInSeconds, GET /:slug answers 410 {"error": "expired"} once it has passed and the expired
// visit is not counted; links without the field never expire.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CANARY, FAKE_TIME, start, stopAll } from "./helpers.js";

beforeEach(() => {
  vi.useFakeTimers({ toFake: [...FAKE_TIME], now: new Date("2031-03-01T10:00:00Z") });
});
afterEach(async () => {
  await stopAll();
  vi.useRealTimers();
});

describe(`holdout #6 links that expire (${CANARY})`, () => {
  it("redirects before the expiry and answers 410 {error: 'expired'} after it", async () => {
    const app = await start();
    const created = await app.post({ url: "https://example.com/soon", expiresInSeconds: 60 });
    expect(created.status).toBe(201);
    const { slug } = created.json();
    vi.advanceTimersByTime(59_000);
    const before = await app.get(`/${slug}`);
    expect(before.status).toBe(302);
    expect(before.location).toBe("https://example.com/soon");
    vi.advanceTimersByTime(2_000);
    const after = await app.get(`/${slug}`);
    expect(after.status).toBe(410);
    expect(after.json()).toEqual({ error: "expired" });
  });

  it("an expired visit is not counted", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/count", expiresInSeconds: 30 })).json();
    expect((await app.get(`/${slug}`)).status).toBe(302);
    vi.advanceTimersByTime(31_000);
    expect((await app.get(`/${slug}`)).status).toBe(410);
    expect((await app.get(`/${slug}`)).status).toBe(410);
    // The issue does not say what stats answer for an expired link; when they answer 200 they show one click.
    const stats = await app.get(`/stats/${slug}`);
    if (stats.status === 200) expect(stats.json().clicks).toBe(1);
    else expect([404, 410]).toContain(stats.status);
  });

  it("links without expiresInSeconds never expire", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/forever" })).json();
    vi.advanceTimersByTime(2 * 86_400_000);
    // A jump of the wall clock with no timers fired: it can only make a timer-based expiry expire less.
    vi.setSystemTime(new Date("2041-03-01T10:00:00Z"));
    const hop = await app.get(`/${slug}`);
    expect(hop.status).toBe(302);
    expect(hop.location).toBe("https://example.com/forever");
  });
});
