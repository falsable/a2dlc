// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #5: a custom slug must match ^[a-z0-9-]{3,32}$ (else 400); a taken slug answers 409 and leaves the existing
// link untouched; without a slug a random one is still generated.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, slugs, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #5 custom slugs (${CANARY})`, () => {
  it("creates the link under the requested slug", async () => {
    const app = await start();
    const created = await app.post({ url: "https://example.com/launch", slug: "my-launch" });
    expect(created.status).toBe(201);
    expect(created.json().slug).toBe("my-launch");
    expect((await app.get("/my-launch")).location).toBe("https://example.com/launch");
  });

  it("accepts slugs of exactly 3 and exactly 32 characters", async () => {
    const app = await start();
    const s32 = "a1-".repeat(10) + "zz";
    for (const slug of ["abc", s32]) {
      const created = await app.post({ url: "https://example.com/len", slug });
      expect(created.status, slug).toBe(201);
      expect((await app.get(`/${slug}`)).location).toBe("https://example.com/len");
    }
  });

  it("rejects slugs that do not match with 400 and creates nothing", async () => {
    const app = await start();
    const bad = ["ab", "b".repeat(33), "My-Launch", "my launch", "my_launch", "my.launch", "launch!"];
    for (const slug of bad) {
      const res = await app.post({ url: "https://example.com/bad", slug });
      expect(res.status, slug).toBe(400);
    }
    expect((await app.get("/My-Launch")).status).toBe(404);
    expect((await app.get(`/${"b".repeat(33)}`)).status).toBe(404);
  });

  it("a taken slug answers 409 and the existing link is untouched", async () => {
    const app = await start();
    expect((await app.post({ url: "https://first.example.com/", slug: "taken-slug" })).status).toBe(201);
    const again = await app.post({ url: "https://second.example.com/", slug: "taken-slug" });
    expect(again.status).toBe(409);
    expect((await app.get("/taken-slug")).location).toBe("https://first.example.com/");
  });

  it("without a slug a random one is still generated", async () => {
    const app = await start({ slugFn: slugs("rnd123") });
    const created = await app.post({ url: "https://example.com/random" });
    expect(created.status).toBe(201);
    expect(created.json().slug).toBe("rnd123");
    expect((await app.get("/rnd123")).location).toBe("https://example.com/random");
  });
});
