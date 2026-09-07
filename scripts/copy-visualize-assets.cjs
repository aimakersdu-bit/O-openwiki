/**
 * Copies browser assets that TypeScript does not emit into the visualizer
 * distribution directory. The list is explicit so adding another asset is a
 * one-line change, and every copy is verified before the build can succeed.
 */
const { copyFileSync, existsSync, mkdirSync, statSync } = require("node:fs");
const path = require("node:path");

const DIST_VISUALIZE_DIR = path.resolve(__dirname, "..", "dist", "visualize");

function packageRoot(packageName) {
  let current = path.dirname(require.resolve(packageName));
  while (current !== path.dirname(current)) {
    if (existsSync(path.join(current, "package.json"))) return current;
    current = path.dirname(current);
  }
  throw new Error(`package root not found: ${packageName}`);
}

function packageFile(packageName, ...segments) {
  return path.join(packageRoot(packageName), ...segments);
}

/** Browser assets required by the compiled visualizer. */
const ASSETS = [
  {
    source: path.resolve(__dirname, "..", "src", "visualize", "styles.css"),
    destination: path.resolve(
      __dirname,
      "..",
      "dist",
      "visualize",
      "styles.css",
    ),
  },
  {
    source: path.resolve(__dirname, "..", "src", "visualize", "fonts.css"),
    destination: path.resolve(
      __dirname,
      "..",
      "dist",
      "visualize",
      "fonts.css",
    ),
  },
  {
    source: packageFile("force-graph", "dist", "force-graph.min.js"),
    destination: path.resolve(
      DIST_VISUALIZE_DIR,
      "vendor",
      "force-graph.min.js",
    ),
  },
  {
    source: packageFile("marked-visualizer", "marked.min.js"),
    destination: path.resolve(DIST_VISUALIZE_DIR, "vendor", "marked.min.js"),
  },
  {
    source: packageFile("dompurify", "dist", "purify.min.js"),
    destination: path.resolve(DIST_VISUALIZE_DIR, "vendor", "purify.min.js"),
  },
  {
    source: packageFile("mermaid", "dist", "mermaid.min.js"),
    destination: path.resolve(DIST_VISUALIZE_DIR, "vendor", "mermaid.min.js"),
  },
  ...[400, 500, 600, 700, 800].map((weight) => ({
    source: packageFile(
      "@fontsource/inter",
      "files",
      `inter-latin-${weight}-normal.woff2`,
    ),
    destination: path.resolve(
      DIST_VISUALIZE_DIR,
      "vendor",
      "fonts",
      `inter-latin-${weight}-normal.woff2`,
    ),
  })),
];

/** Copy one asset and fail if the resulting destination is unusable. */
function copyAsset({ source, destination }) {
  if (!existsSync(source)) {
    throw new Error(`source asset is missing: ${source}`);
  }
  // tsc creates dist/visualize/ on its way to client.js, so a missing directory
  // means this ran without (or before) a build rather than that a copy failed.
  const destinationDir = path.dirname(destination);
  if (!existsSync(destinationDir)) {
    throw new Error(
      `destination directory does not exist (run the build first): ${destinationDir}`,
    );
  }
  copyFileSync(source, destination);
  if (!existsSync(destination) || statSync(destination).size === 0) {
    throw new Error(`destination asset is missing or empty: ${destination}`);
  }
}

function main() {
  if (!existsSync(DIST_VISUALIZE_DIR)) {
    throw new Error(
      `destination directory does not exist (run the build first): ${DIST_VISUALIZE_DIR}`,
    );
  }
  for (const asset of ASSETS)
    mkdirSync(path.dirname(asset.destination), { recursive: true });
  for (const asset of ASSETS) copyAsset(asset);
  const noun = ASSETS.length === 1 ? "asset" : "assets";
  console.log(`copy-visualize-assets: copied ${ASSETS.length} ${noun}`);
}

if (require.main === module) {
  try {
    main();
  } catch (error) {
    console.error(
      `copy-visualize-assets failed: ${
        error instanceof Error ? error.message : String(error)
      }`,
    );
    process.exit(1);
  }
}

module.exports = { ASSETS, copyAsset };
