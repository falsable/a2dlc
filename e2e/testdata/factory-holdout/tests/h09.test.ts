// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #9: POST /links is limited to 10 requests per minute per client IP; the 11th inside the window answers 429
// with retry-after in seconds; redirects and stats are not limited. Clients are distinct loopback source addresses.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type App, CANARY, FAKE_TIME, start, stopAll } from "./helpers.js";

const A = "127.2.0.1";
const B = "127.2.0.2";

beforeEach(() => {
  vi.useFakeTimers({ toFake: [...FAKE_TIME], now: new Date("2031-07-01T12:00:00Z") });
});
afterEach(async () => {
  await stopAll();
  vi.useRealTimers();
});

async function tenFrom(app: App, client: string, stepMs = 0): Promise<string> {
  let slug = "";
  for (let i = 0; i < 10; i++) {
    const res = await app.post({ url: `https://example.com/rl/${i}` }, client);
    expect(res.status, `request ${i + 1}`).toBe(201);
    slug = res.json().slug;
    if (stepMs) vi.advanceTimersByTime(stepMs);
  }
  return slug;
}

describe(`holdout #9 rate limit link creation per client (${CANARY})`, () => {
  it("the 11th POST inside a minute answers 429 with retry-after in seconds", async () => {
    const app = await start();
    await tenFrom(app, A, 5_000);
    const res = await app.post({ url: "https://example.com/rl/11" }, A);
    expect(res.status).toBe(429);
    const retry = res.header("retry-after") ?? "";
    expect(retry).toMatch(/^\d+$/);
    expect(Number(retry)).toBeGreaterThanOrEqual(1);
    expect(Number(retry)).toBeLessThanOrEqual(60);
  });

  it("another client is not limited by the first one", async () => {
    const app = await start();
    await tenFrom(app, A);
    expect((await app.post({ url: "https://example.com/rl/a11" }, A)).status).toBe(429);
    expect((await app.post({ url: "https://example.com/rl/b1" }, B)).status).toBe(201);
  });

  it("redirects and stats are not limited", async () => {
    const app = await start();
    const slug = await tenFrom(app, A);
    expect((await app.post({ url: "https://example.com/rl/a11" }, A)).status).toBe(429);
    for (let i = 0; i < 15; i++) expect((await app.get(`/${slug}`, A)).status).toBe(302);
    for (let i = 0; i < 5; i++) expect((await app.get(`/stats/${slug}`, A)).status).toBe(200);
  });

  it("the client can create again once the minute has passed", async () => {
    const app = await start();
    await tenFrom(app, A);
    expect((await app.post({ url: "https://example.com/rl/a11" }, A)).status).toBe(429);
    vi.advanceTimersByTime(61_000);
    expect((await app.post({ url: "https://example.com/rl/a12" }, A)).status).toBe(201);
  });
});
