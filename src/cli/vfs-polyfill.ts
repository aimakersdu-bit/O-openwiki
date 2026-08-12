import fs from "node:fs";

// Polyfill for Bun standalone binaries: prevent third-party modules (like bindings)
// from throwing "Could not find module root" when walking up /$bunfs directories.
try {
  const origExistsSync = fs.existsSync;
  if (typeof origExistsSync === "function") {
    fs.existsSync = function (p: any) {
      if (
        typeof p === "string" &&
        (p.includes("$bunfs") || p.startsWith("/$bunfs")) &&
        (p.endsWith("package.json") || p.endsWith("node_modules"))
      ) {
        return true;
      }
      return origExistsSync.apply(this, arguments as any);
    };
  }

  const origAccessSync = fs.accessSync;
  if (typeof origAccessSync === "function") {
    fs.accessSync = function (p: any, mode?: any) {
      if (
        typeof p === "string" &&
        (p.includes("$bunfs") || p.startsWith("/$bunfs")) &&
        (p.endsWith("package.json") || p.endsWith("node_modules"))
      ) {
        return;
      }
      return origAccessSync.apply(this, arguments as any);
    };
  }

  const origStatSync = fs.statSync;
  if (typeof origStatSync === "function") {
    fs.statSync = function (p: any, options?: any) {
      if (
        typeof p === "string" &&
        (p.includes("$bunfs") || p.startsWith("/$bunfs")) &&
        (p.endsWith("package.json") || p.endsWith("node_modules"))
      ) {
        return { isFile: () => true, isDirectory: () => true } as any;
      }
      return origStatSync.apply(this, arguments as any);
    };
  }
} catch (e) {}
