import { execFile, spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import path from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Two consecutive builds, one warm `.next`, and a release that changed in
 * between: the export must name the new release.
 *
 * This is the slow, literal version of the bug — it runs `npm run build` twice,
 * exactly as a developer or a self-hosted runner with a restored `.next` cache
 * would. Before the fix the second build made **zero** API requests and both
 * exports said 0.9.0 while the API said 0.10.0; the deploy published that page
 * and only a human looking at the site noticed.
 *
 * It is opt-in (`npm run test:build-cache`, `CCRESU_BUILD_CACHE_TEST=1`) because
 * it builds the site twice — roughly a minute — and the default suite is meant
 * to stay fast. The fast parts of the same contract are in
 * `tests/release-generation.test.ts`.
 *
 * The fake endpoint answers with the version `web/package.json` declares on the
 * *second* build, so the tree it leaves behind is the one the deploy's version
 * guard expects.
 */

const enabled = process.env.CCRESU_BUILD_CACHE_TEST === "1";
const webRoot = path.resolve(import.meta.dirname, "..");

/** The version the export advertises, read from the archive names it prints. */
function exportedVersion(): string[] {
  const html = readFileSync(path.join(webRoot, "out", "index.html"), "utf8");
  return [...new Set([...html.matchAll(/ccresu_([0-9][0-9A-Za-z.+-]*)_linux_amd64/g)].map((m) => m[1]))];
}

function declaredVersion(): string {
  return (JSON.parse(readFileSync(path.join(webRoot, "package.json"), "utf8")) as { version: string })
    .version;
}

function build(
  env: Record<string, string>,
): Promise<{ status: number; stdout: string; stderr: string }> {
  const base = { ...process.env };
  delete base.CCRESU_RELEASE_TAG;
  delete base.CCRESU_RELEASE_FILE;

  return new Promise((resolve) => {
    // Asynchronous on purpose: the fake release API runs in this process, so a
    // blocking spawn would starve the event loop that has to answer the build.
    execFile(
      "npm",
      ["run", "build"],
      { cwd: webRoot, encoding: "utf8", timeout: 540_000, env: { ...base, ...env } },
      (error, stdout, stderr) => {
        const status =
          error === null ? 0 : typeof error.code === "number" ? error.code : -1;
        resolve({ status, stdout: stdout ?? "", stderr: stderr ?? "" });
      },
    );
  });
}

describe.skipIf(!enabled)("a warm .next cannot bake the previous build's version", () => {
  it(
    "rebuilds with the release the API answers this time",
    async () => {
      let tag = "v0.9.0";
      let hits = 0;
      const server: Server = createServer((_req, res) => {
        hits += 1;
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ tag_name: tag }));
      });
      await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
      const address = server.address();
      if (address === null || typeof address === "string") {
        throw new Error("no address for the fake release API");
      }
      const endpoint = `http://127.0.0.1:${address.port}/releases/latest`;

      try {
        // A cold first build, so the second one is unambiguously warm.
        spawnSync("rm", ["-rf", ".next", "out"], { cwd: webRoot });

        const first = await build({ CCRESU_RELEASE_API: endpoint });
        expect(first.status, first.stderr).toBe(0);
        expect(exportedVersion()).toEqual(["0.9.0"]);

        // The release is published between the two builds.
        tag = `v${declaredVersion()}`;
        const second = await build({ CCRESU_RELEASE_API: endpoint });
        expect(second.status, second.stderr).toBe(0);

        // The claim: the second build asks again rather than reusing the first
        // answer, and the export follows the answer.
        expect(hits).toBeGreaterThanOrEqual(2);
        expect(exportedVersion()).toEqual([declaredVersion()]);
        expect(existsSync(path.join(webRoot, ".next"))).toBe(true);
      } finally {
        await new Promise<void>((resolve, reject) => {
          server.close((error) => (error ? reject(error) : resolve()));
        });
      }
    },
    600_000,
  );
});
