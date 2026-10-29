// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Issue #7: DELETE /links/:slug answers 204 when the link existed and 404 otherwise; afterwards GET /:slug and
// GET /stats/:slug both answer 404.
import { afterEach, describe, expect, it } from "vitest";
import { CANARY, start, stopAll } from "./helpers.js";

afterEach(stopAll);

describe(`holdout #7 delete a link (${CANARY})`, () => {
  it("answers 204 for an existing link and 404 for an unknown or already deleted one", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/del" })).json();
    expect((await app.del(`/links/${slug}`)).status).toBe(204);
    expect((await app.del(`/links/${slug}`)).status).toBe(404);
    expect((await app.del("/links/neverwas")).status).toBe(404);
  });

  it("after DELETE the redirect and the stats answer 404", async () => {
    const app = await start();
    const { slug } = (await app.post({ url: "https://example.com/gone" })).json();
    expect((await app.get(`/${slug}`)).status).toBe(302);
    expect((await app.del(`/links/${slug}`)).status).toBe(204);
    expect((await app.get(`/${slug}`)).status).toBe(404);
    expect((await app.get(`/stats/${slug}`)).status).toBe(404);
  });

  it("removes only that link", async () => {
    const app = await start();
    const a = (await app.post({ url: "https://a.example.com/" })).json().slug;
    const b = (await app.post({ url: "https://b.example.com/" })).json().slug;
    expect((await app.del(`/links/${a}`)).status).toBe(204);
    const hop = await app.get(`/${b}`);
    expect(hop.status).toBe(302);
    expect(hop.location).toBe("https://b.example.com/");
    expect((await app.get(`/stats/${b}`)).status).toBe(200);
  });
});
