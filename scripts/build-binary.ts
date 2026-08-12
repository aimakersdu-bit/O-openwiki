import { chmodSync, copyFileSync, existsSync, mkdirSync, unlinkSync } from "node:fs";
import path from "node:path";

let target = process.argv[2];
if (!target || target === "bun") {
  target = `bun-${process.platform}-${process.arch}`;
}
const outfile = path.resolve(process.cwd(), process.argv[3] || "./dist-bin/openwiki-bin");
const outDir = path.dirname(outfile);

if (!existsSync(outDir)) {
  mkdirSync(outDir, { recursive: true });
}

const stubBindingsPath = path.resolve(__dirname, "../src/stubs/safe-bindings-stub.js");
const stubBetterSqlitePath = path.resolve(__dirname, "../src/stubs/better-sqlite3.ts");
const stubReactDevtoolsPath = path.resolve(__dirname, "../src/stubs/react-devtools-core.ts");

const stubPlugin = {
  name: "stub-plugin",
  setup(build: any) {
    build.onResolve({ filter: /^react-devtools-core$/ }, () => ({ path: stubReactDevtoolsPath }));
    build.onResolve({ filter: /^better-sqlite3$/ }, () => ({ path: stubBetterSqlitePath }));
    build.onResolve({ filter: /^bindings$/ }, () => ({ path: stubBindingsPath }));
  },
};

console.log(`Building standalone OpenWiki binary for target ${target} -> ${outfile}...`);

const result = await Bun.build({
  entrypoints: ["./src/cli/cli.tsx"],
  compile: true,
  target: target as any,
  plugins: [stubPlugin],
  alias: {
    "bindings": stubBindingsPath,
    "better-sqlite3": stubBetterSqlitePath,
    "react-devtools-core": stubReactDevtoolsPath,
  },
  minify: false,
});

if (!result.success || !result.outputs || result.outputs.length === 0) {
  console.error("Build failed:", result.logs);
  process.exit(1);
}

const generatedPath = result.outputs[0].path;
if (existsSync(generatedPath)) {
  copyFileSync(generatedPath, outfile);
  if (generatedPath !== outfile) {
    try {
      unlinkSync(generatedPath);
    } catch (e) {}
  }
}

if (!outfile.endsWith(".exe")) {
  chmodSync(outfile, 0o755);
}
console.log(`Successfully built standalone binary at ${outfile}`);
