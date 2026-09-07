import {
  mkdtemp,
  mkdir,
  readdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, expect, test } from "vitest";
import { exportStaticVisualizer } from "../../src/visualize/static-export.ts";

const tempDirs: string[] = [];

async function makeTempDir(): Promise<string> {
  const dir = await mkdtemp(path.join(tmpdir(), "openwiki-static-viz-"));
  tempDirs.push(dir);
  return dir;
}

afterEach(async () => {
  await Promise.all(
    tempDirs.splice(0).map((dir) => rm(dir, { force: true, recursive: true })),
  );
});

test("exports a static visualizer with sibling graph and browser assets", async () => {
  const root = await makeTempDir();
  const wikiRoot = path.join(root, "wiki");
  const outputDir = path.join(root, "docs", "visualizer");
  await mkdir(wikiRoot);
  await writeFile(
    path.join(wikiRoot, "quickstart.md"),
    "---\ntitle: Quickstart\ntype: Guide\n---\n# Quickstart\n\nSee [Overview](overview.md).\n",
  );
  await writeFile(
    path.join(wikiRoot, "overview.md"),
    "---\ntitle: Overview\ntype: Guide\n---\n# Overview\n",
  );

  const result = await exportStaticVisualizer({
    wikiRoot,
    outputDir,
    assets: {
      clientJs: 'export const client = "static";\n',
      clientLibJs: 'export const library = "static";\n',
      stylesCss: "/* styles */\n",
      fontsCss: "/* fonts */\n",
      vendorAssets: [
        {
          path: "vendor/force-graph.min.js",
          contentType: "text/javascript; charset=utf-8",
          body: Buffer.from("/* force graph */\n"),
        },
        {
          path: "vendor/fonts/inter-latin-400-normal.woff2",
          contentType: "font/woff2",
          body: Buffer.from("font"),
        },
      ],
    },
  });

  expect(result.outputDir).toBe(outputDir);
  expect(result.graph.nodes).toHaveLength(2);
  expect(result.graph.edges).toEqual([
    { source: "quickstart", target: "overview" },
  ]);
  expect(await readdir(outputDir)).toEqual([
    "client-lib.js",
    "client.js",
    "fonts.css",
    "graph.json",
    "index.html",
    "styles.css",
    "vendor",
  ]);
  expect(await readFile(path.join(outputDir, "client.js"), "utf8")).toBe(
    'export const client = "static";\n',
  );
  expect(await readFile(path.join(outputDir, "client-lib.js"), "utf8")).toBe(
    'export const library = "static";\n',
  );
  expect(await readFile(path.join(outputDir, "styles.css"), "utf8")).toBe(
    "/* styles */\n",
  );
  expect(await readFile(path.join(outputDir, "fonts.css"), "utf8")).toBe(
    "/* fonts */\n",
  );
  expect(
    await readFile(
      path.join(outputDir, "vendor", "force-graph.min.js"),
      "utf8",
    ),
  ).toBe("/* force graph */\n");
  expect(
    await readFile(
      path.join(outputDir, "vendor", "fonts", "inter-latin-400-normal.woff2"),
      "utf8",
    ),
  ).toBe("font");

  const page = await readFile(path.join(outputDir, "index.html"), "utf8");
  expect(page).toContain('data-static-export="true"');
  expect(page).toContain('src="./client.js"');
  expect(page).toContain('src="./vendor/force-graph.min.js"');
  expect(page).toContain('href="./fonts.css"');
  expect(page).toContain('id="live-text">Static<');
  expect(page).toContain(`http-equiv="Content-Security-Policy"`);

  expect(
    JSON.parse(await readFile(path.join(outputDir, "graph.json"), "utf8")),
  ).toEqual(result.graph);
});
