import { createRequire } from 'node:module';

// Polyfill globalThis.require for Node.js ESM execution (redirect bun:sqlite -> better-sqlite3)
if (typeof globalThis.require === 'undefined') {
  const nativeRequire = createRequire(import.meta.url);
  globalThis.require = (id) => {
    if (id === 'bun:sqlite') {
      return { Database: nativeRequire('better-sqlite3') };
    }
    return nativeRequire(id);
  };
}

// ESM loader hook for Node.js v22 JSON import attribute resolution
export async function load(url, context, nextLoad) {
  if (url.endsWith('.json') && (!context.importAttributes || !context.importAttributes.type)) {
    return nextLoad(url, {
      ...context,
      importAttributes: { ...(context.importAttributes || {}), type: 'json' }
    });
  }
  return nextLoad(url, context);
}
