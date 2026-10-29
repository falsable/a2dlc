// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #8: GET /links returns {items, nextCursor} in creation order, ?limit= (default 20, max 100) and ?cursor= from
// the previous page; the last page has nextCursor null. The clock is frozen, so every link has the same creation
// time, and the slugs are created in descending order: neither a sort by slug nor a tie-break by slug is creation order.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type App, CANARY, FAKE_TIME, start, stopAll } from "./helpers.js";

beforeEach(() => {
  vi.useFakeTimers({ toFake: [...FAKE_TIME], now: new Date("2031-05-01T08:00:00Z") });
});
afterEach(async () => {
  await stopAll();
  vi.useRealTimers();
});

const descending = () => {
  let i = 0;
  return () => `lnk${String(999 - i++).padStart(3, "0")}`;
};

async function fill(app: App, n: number): Promise<string[]> {
  const made: string[] = [];
  for (let i = 0; i < n; i++) {
    const res = await app.post({ url: `https://example.com/item/${i}` });
    expect(res.status).toBe(201);
    made.push(res.json().slug);
  }
  return made;
}

async function page(app: App, query: string): Promise<{ slugs: string[]; urls: string[]; next: unknown }> {
  const res = await app.get(`/links${query}`);
  expect(res.status).toBe(200);
  const body = res.json();
  expect(Array.isArray(body.items)).toBe(true);
  return { slugs: body.items.map((l: any) => l.slug), urls: body.items.map((l: any) => l.url), next: body.nextCursor };
}

describe(`holdout #8 list links with cursor pagination (${CANARY})`, () => {
  it("lists in creation order, 20 per page by default, and the last page has nextCursor null", async () => {
    const app = await start({ slugFn: descending() });
    expect(await page(app, "")).toEqual({ slugs: [], urls: [], next: null });
    const made = await fill(app, 25);
    const p1 = await page(app, "");
    expect(p1.slugs).toEqual(made.slice(0, 20));
    expect(p1.urls[0]).toBe("https://example.com/item/0");
    expect(typeof p1.next).toBe("string");
    const p2 = await page(app, `?cursor=${encodeURIComponent(p1.next as string)}`);
    expect(p2.slugs).toEqual(made.slice(20));
    expect(p2.next).toBeNull();
  });

  it("walks every page with ?limit= and returns each link once", async () => {
    const app = await start({ slugFn: descending() });
    const made = await fill(app, 23);
    const seen: string[] = [];
    const sizes: number[] = [];
    let query = "?limit=5";
    for (let guard = 0; guard < 10; guard++) {
      const p = await page(app, query);
      seen.push(...p.slugs);
      sizes.push(p.slugs.length);
      if (p.next === null) break;
      expect(typeof p.next).toBe("string");
      query = `?limit=5&cursor=${encodeURIComponent(p.next as string)}`;
    }
    expect(sizes).toEqual([5, 5, 5, 5, 3]);
    expect(seen).toEqual(made);
  });

  it("caps ?limit= at 100", async () => {
    const app = await start({ slugFn: descending() });
    const made = await fill(app, 105);
    const p1 = await page(app, "?limit=500");
    expect(p1.slugs).toEqual(made.slice(0, 100));
    expect(typeof p1.next).toBe("string");
    const p2 = await page(app, `?limit=500&cursor=${encodeURIComponent(p1.next as string)}`);
    expect(p2.slugs).toEqual(made.slice(100));
    expect(p2.next).toBeNull();
  });
});
