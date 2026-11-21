import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { afterAll, describe, expect, it } from "vitest";

/**
 * The version is resolved by a script, before the build — and the script has no
 * memory.
 *
 * The bug this file exists for: `fetch` called from a page render was served out
 * of Next's build data cache, so a build that inherited a warm `.next` reused
 * whatever the *first* build had seen. Two consecutive builds against a changing
 * API made zero requests the second time and both baked 0.9.0 while the API
 * answered 0.10.0 — and the deploy published the stale page.
 *
 * So these tests drive the real script in a real child process against a real
 * HTTP server, and assert the two properties that fix it:
 *
 *  - a second run asks again (the request count goes up rather than the previous
 *    answer being reused), and
 *  - the file it writes says what the endpoint said that time.
 *
 * A handed tag (`CCRESU_RELEASE_TAG`) must still cost no request at all, which is
 * what makes a release-triggered deploy unable to race the API.
 */

const webRoot = path.resolve(import.meta.dirname, "..");
const script = path.join(webRoot, "scripts", "resolve-release.mjs");
const scratch = mkdtempSync(path.join(tmpdir(), "ccresu-resolve-"));

afterAll(() => {
  rmSync(scratch, { recursive: true, force: true });
});

type Answer = { status: number; body: unknown };

/** A fake `releases/latest` whose answer can change between runs. */
async function fakeApi(): Promise<{
  url: string;
  hits: () => number;
  answer: (next: Answer) => void;
  close: () => Promise<void>;
}> {
  let current: Answer = { status: 200, body: { tag_name: "v0.9.0" } };
  let hits = 0;

  const server: Server = createServer((req, res) => {
    hits += 1;
    res.writeHead(current.status, { "content-type": "application/json" });
    res.end(JSON.stringify(current.body));
    void req;
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("no address for the fake release API");
  }

  return {
    url: `http://127.0.0.1:${address.port}/releases/latest`,
    hits: () => hits,
    answer: (next) => {
      current = next;
    },
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
      }),
  };
}

/** Run the generator the way the build does, with a clean environment. */
function resolveRelease(
  out: string,
  env: Record<string, string> = {},
): Promise<{ status: number; stdout: string; stderr: string }> {
  const base = { ...process.env };
  delete base.CCRESU_RELEASE_API;
  delete base.CCRESU_RELEASE_TAG;
  delete base.CCRESU_RELEASE_FILE;

  return new Promise((resolve) => {
    // Asynchronous on purpose: the fake API lives in this process, so a blocking
    // spawn would stop the event loop that has to answer the child's request.
    execFile(
      process.execPath,
      [script, "--out", out],
      { cwd: webRoot, encoding: "utf8", env: { ...base, ...env }, timeout: 30_000 },
      (error, stdout, stderr) => {
        const status =
          error === null ? 0 : typeof error.code === "number" ? error.code : -1;
        resolve({ status, stdout: stdout ?? "", stderr: stderr ?? "" });
      },
    );
  });
}

function record(out: string): Record<string, unknown> {
  return JSON.parse(readFileSync(out, "utf8")) as Record<string, unknown>;
}

describe("the release is resolved before the build, and nothing is remembered", () => {
  it("asks again on the next run instead of reusing the previous answer", async () => {
    const api = await fakeApi();
    const out = path.join(scratch, "consecutive.json");
    try {
      // First run: the API still names the previous release.
      const first = await resolveRelease(out, { CCRESU_RELEASE_API: api.url });
      expect(first.status).toBe(0);
      expect(record(out)).toMatchObject({ source: "github", version: "0.9.0", tag: "v0.9.0" });
      expect(api.hits()).toBe(1);

      // The release is published; the next run must see it. This is the exact
      // step that used to be served from the build cache.
      api.answer({ status: 200, body: { tag_name: "v0.10.0" } });
      const second = await resolveRelease(out, { CCRESU_RELEASE_API: api.url });
      expect(second.status).toBe(0);
      expect(record(out)).toMatchObject({ source: "github", version: "0.10.0", tag: "v0.10.0" });

      // Two runs, two requests: no answer was carried over.
      expect(api.hits()).toBe(2);
    } finally {
      await api.close();
    }
  });

  it("records how the version was decided, so a fallback is visible", async () => {
    const api = await fakeApi();
    const out = path.join(scratch, "provenance.json");
    try {
      await resolveRelease(out, { CCRESU_RELEASE_API: api.url });
      const written = record(out);
      expect(written).toMatchObject({ source: "github", endpoint: api.url, reason: null });
      expect(typeof written.resolvedAt).toBe("string");
      expect(written.generator).toBe("scripts/resolve-release.mjs");
    } finally {
      await api.close();
    }
  });

  it("costs no request at all when the deploy hands it the tag", async () => {
    const api = await fakeApi();
    const out = path.join(scratch, "pinned.json");
    try {
      api.answer({ status: 200, body: { tag_name: "v0.9.2" } });

      const result = await resolveRelease(out, {
        CCRESU_RELEASE_API: api.url,
        CCRESU_RELEASE_TAG: "v0.10.0",
      });

      expect(result.status).toBe(0);
      expect(record(out)).toMatchObject({ source: "tag", version: "0.10.0", tag: "v0.10.0" });
      // The whole point of the pin: a release-triggered deploy cannot be told
      // about the previous release.
      expect(api.hits()).toBe(0);
    } finally {
      await api.close();
    }
  });

  it("records the reason instead of failing the build when the API refuses", async () => {
    const api = await fakeApi();
    const out = path.join(scratch, "refused.json");
    try {
      api.answer({ status: 403, body: { message: "API rate limit exceeded" } });

      const result = await resolveRelease(out, { CCRESU_RELEASE_API: api.url });

      // A build that cannot run is worse than a page naming the last version we
      // knew about; the deploy's version guard is what refuses to publish.
      expect(result.status).toBe(0);
      const written = record(out);
      expect(written.source).toBe("unavailable");
      expect(written.version).toBeNull();
      expect(String(written.reason)).toContain("403");
    } finally {
      await api.close();
    }
  });

  it("records the reason when the network is dead", async () => {
    const out = path.join(scratch, "offline.json");
    // Port 1 on loopback: nothing listens there, so the request fails immediately.
    const result = await resolveRelease(out, { CCRESU_RELEASE_API: "http://127.0.0.1:1/latest" });

    expect(result.status).toBe(0);
    const written = record(out);
    expect(written.source).toBe("unavailable");
    expect(String(written.reason)).toMatch(/request failed/);
  });

  it("is wired into the build, not just available", () => {
    const manifest = JSON.parse(readFileSync(path.join(webRoot, "package.json"), "utf8")) as {
      scripts: Record<string, string>;
    };

    // `npm run build` must regenerate the record before Next starts. Without
    // this, a build silently reuses whatever file is on disk.
    expect(manifest.scripts.prebuild).toBe("node scripts/resolve-release.mjs");
    expect(manifest.scripts.predev).toBe("node scripts/resolve-release.mjs");
    expect(manifest.scripts["resolve-release"]).toBe("node scripts/resolve-release.mjs");
  });

  it("keeps the resolution out of the render, where the cache could answer", () => {
    for (const route of ["app/page.tsx", path.join("app", "docs", "installation", "page.tsx")]) {
      const source = readFileSync(path.join(webRoot, route), "utf8");
      expect(source).toContain("resolvedRelease");
      // A page that fetched at render time is what the bug was made of.
      expect(source).not.toContain("resolveLatestRelease");
      expect(source).not.toContain("fetch(");
      // Belt and braces: if a future edit makes the route dynamic, the static
      // export must fail the build rather than render something else.
      expect(source).toContain('export const dynamic = "force-static"');
    }

    const lib = readFileSync(path.join(webRoot, "lib", "release.ts"), "utf8");
    expect(lib).not.toContain("fetchLatestRelease");
    expect(lib).not.toContain("await fetch");
  });
});
