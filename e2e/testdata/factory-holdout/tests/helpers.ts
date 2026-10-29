// A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line
// Factory holdout, sealed (see ../README.md). The only thing taken from the app is createApp and its slugFn option,
// which the base app already has; store.ts and url.ts may be rewritten by a builder. Requests go through node:http,
// one loopback source address per call unless a test names a client, so a per-client rate limiter (#9) in an
// integrated tree never limits the other issues' tests.
import { request } from "node:http";
import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { createApp } from "../src/server.js";

export const CANARY = "A2DLC HOLDOUT CANARY bead 3fu.2 purple-walrus-rides-a-tram: a lane must never see this line";

export interface Reply {
  status: number;
  location: string | undefined;
  header(name: string): string | undefined;
  text: string;
  json(): any;
}

export interface App {
  post(body: unknown, client?: string): Promise<Reply>;
  get(path: string, client?: string): Promise<Reply>;
  head(path: string, client?: string): Promise<Reply>;
  del(path: string, client?: string): Promise<Reply>;
}

let calls = 0;
/** A loopback address no other call of this file has used (127.1.0.1, 127.1.0.2, ...). */
export function freshClient(): string {
  const n = calls++;
  return `127.1.${Math.floor(n / 250) % 250}.${(n % 250) + 1}`;
}

function call(port: number, method: string, path: string, body: unknown, client: string): Promise<Reply> {
  return new Promise((resolve, reject) => {
    const data = body === undefined ? undefined : JSON.stringify(body);
    const headers: Record<string, string | number> = { "user-agent": CANARY };
    if (data !== undefined) Object.assign(headers, { "content-type": "application/json", "content-length": Buffer.byteLength(data) });
    const req = request({ host: "127.0.0.1", port, method, path, localAddress: client, agent: false, headers }, (res) => {
      let text = "";
      res.setEncoding("utf8");
      res.on("data", (c) => (text += c));
      res.on("end", () => {
        const one = (name: string) => {
          const v = res.headers[name.toLowerCase()];
          return Array.isArray(v) ? v.join(", ") : v;
        };
        resolve({ status: res.statusCode ?? 0, location: one("location"), header: one, text, json: () => JSON.parse(text) });
      });
    });
    req.on("error", reject);
    if (data !== undefined) req.write(data);
    req.end();
  });
}

const running: Server[] = [];

export async function start(opts: { slugFn?: () => string } = {}): Promise<App> {
  const server = createApp(opts);
  running.push(server);
  await new Promise<void>((r) => server.listen(0, r));
  const { port } = server.address() as AddressInfo;
  return {
    post: (body, client = freshClient()) => call(port, "POST", "/links", body, client),
    get: (path, client = freshClient()) => call(port, "GET", path, undefined, client),
    head: (path, client = freshClient()) => call(port, "HEAD", path, undefined, client),
    del: (path, client = freshClient()) => call(port, "DELETE", path, undefined, client),
  };
}

export async function stopAll(): Promise<void> {
  for (const server of running.splice(0)) {
    server.closeAllConnections();
    await new Promise<void>((r) => server.close(() => r()));
  }
}

/** A slugFn that returns the given values in order, then distinct values that none of them can equal. */
export function slugs(...values: string[]): () => string {
  let i = 0;
  return () => (i < values.length ? values[i++] : `zzextra${i++}`);
}

/** Time sources a builder may use for windows and expiry; tests move them only with vi.advanceTimersByTime. */
export const FAKE_TIME = ["Date", "performance", "hrtime", "setTimeout", "clearTimeout", "setInterval", "clearInterval"] as const;
