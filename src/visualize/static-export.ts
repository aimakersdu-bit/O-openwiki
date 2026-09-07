import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { buildGraph, type WikiGraph } from "./graph.js";
import { STATIC_PAGE } from "./page.js";

/** Browser assets emitted alongside the server and copied into static exports. */
export interface VisualizerAssets {
  clientJs: string;
  clientLibJs: string;
  /** Stylesheet served verbatim and copied into static exports. */
  stylesCss: string;
  /** Local Inter font-face stylesheet served before the visualizer styles. */
  fontsCss: string;
  /** Third-party browser libraries and fonts served from fixed local paths. */
  vendorAssets: VisualizerVendorAsset[];
}

export interface VisualizerVendorAsset {
  path: string;
  contentType: string;
  body: Uint8Array;
}

/** Inputs for writing a self-contained static visualizer directory. */
export interface StaticVisualizerExportOptions {
  /** Absolute path to the generated wiki that supplies graph data. */
  wikiRoot: string;

  /** Absolute path to the directory receiving the static app. */
  outputDir: string;

  /** Test seam; production callers load the compiled browser modules. */
  assets?: VisualizerAssets;
}

/** Summary of the graph captured by one static export. */
export interface StaticVisualizerExportResult {
  outputDir: string;
  graph: WikiGraph;
}

export const VISUALIZER_VENDOR_ASSET_PATHS = [
  "vendor/force-graph.min.js",
  "vendor/marked.min.js",
  "vendor/purify.min.js",
  "vendor/mermaid.min.js",
  "vendor/fonts/inter-latin-400-normal.woff2",
  "vendor/fonts/inter-latin-500-normal.woff2",
  "vendor/fonts/inter-latin-600-normal.woff2",
  "vendor/fonts/inter-latin-700-normal.woff2",
  "vendor/fonts/inter-latin-800-normal.woff2",
] as const;

/** Read the browser assets that ship beside this module in dist. */
export async function loadVisualizerAssets(): Promise<VisualizerAssets> {
  const [clientJs, clientLibJs, stylesCss, fontsCss] = await Promise.all([
    readFile(new URL("./client.js", import.meta.url), "utf8"),
    readFile(new URL("./client-lib.js", import.meta.url), "utf8"),
    readFile(new URL("./styles.css", import.meta.url), "utf8"),
    readFile(new URL("./fonts.css", import.meta.url), "utf8"),
  ]);
  const vendorAssets = await Promise.all(
    VISUALIZER_VENDOR_ASSET_PATHS.map(async (assetPath) => ({
      path: assetPath,
      contentType: contentTypeForVisualizerAsset(assetPath),
      body: await readFile(new URL(`./${assetPath}`, import.meta.url)),
    })),
  );
  return {
    clientJs,
    clientLibJs,
    stylesCss,
    fontsCss,
    vendorAssets,
  };
}

/**
 * Write the visualizer as sibling static files. The client reads ./graph.json and
 * never opens an SSE connection, so the output can be hosted without OpenWiki.
 */
export async function exportStaticVisualizer(
  options: StaticVisualizerExportOptions,
): Promise<StaticVisualizerExportResult> {
  const [graph, assets] = await Promise.all([
    buildGraph(options.wikiRoot),
    options.assets ? Promise.resolve(options.assets) : loadVisualizerAssets(),
  ]);

  await mkdir(options.outputDir, { recursive: true });
  await Promise.all([
    writeFile(path.join(options.outputDir, "index.html"), STATIC_PAGE, "utf8"),
    writeFile(
      path.join(options.outputDir, "client.js"),
      assets.clientJs,
      "utf8",
    ),
    writeFile(
      path.join(options.outputDir, "client-lib.js"),
      assets.clientLibJs,
      "utf8",
    ),
    writeFile(
      path.join(options.outputDir, "styles.css"),
      assets.stylesCss,
      "utf8",
    ),
    writeFile(
      path.join(options.outputDir, "fonts.css"),
      assets.fontsCss,
      "utf8",
    ),
    ...assets.vendorAssets.map(async (asset) => {
      const destination = path.join(options.outputDir, asset.path);
      await mkdir(path.dirname(destination), { recursive: true });
      await writeFile(destination, asset.body);
    }),
    writeFile(
      path.join(options.outputDir, "graph.json"),
      `${JSON.stringify(graph, null, 2)}\n`,
      "utf8",
    ),
  ]);

  return { outputDir: options.outputDir, graph };
}

function contentTypeForVisualizerAsset(assetPath: string): string {
  if (assetPath.endsWith(".js")) return "text/javascript; charset=utf-8";
  if (assetPath.endsWith(".woff2")) return "font/woff2";
  return "application/octet-stream";
}
