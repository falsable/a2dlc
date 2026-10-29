// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #1: a random slug that is already taken never overwrites the existing link; generation goes on until it is free.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, slugs, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #1 random slugs never overwrite (${CANARY})`, () => {
  it("a colliding random slug does not replace the existing link", async () => {
    const app = await start({ slugFn: slugs("aaaaaa", "aaaaaa", "bbbbbb") });
    const first = await app.post({ url: "https://one.example.com/a" });
    expect(first.status).toBe(201);
    const second = await app.post({ url: "https://two.example.com/b" });
    expect(second.status).toBe(201);
    expect(second.json().slug).not.toBe(first.json().slug);
    expect((await app.get(`/${first.json().slug}`)).location).toBe("https://one.example.com/a");
    expect((await app.get(`/${second.json().slug}`)).location).toBe("https://two.example.com/b");
  });

  it("keeps generating while the slug is taken, five times in a row", async () => {
    const app = await start({ slugFn: slugs("cccccc", "cccccc", "cccccc", "cccccc", "cccccc", "cccccc", "dddddd") });
    const first = await app.post({ url: "https://one.example.com/" });
    expect(first.status).toBe(201);
    const second = await app.post({ url: "https://two.example.com/" });
    expect(second.status).toBe(201);
    expect(second.json().slug).not.toBe("cccccc");
    expect((await app.get("/cccccc")).location).toBe("https://one.example.com/");
    expect((await app.get(`/${second.json().slug}`)).location).toBe("https://two.example.com/");
  });
});
