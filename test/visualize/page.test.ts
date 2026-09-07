import { describe, expect, test } from "vitest";
import { PAGE, STATIC_PAGE } from "../../src/visualize/page.ts";

/**
 * Browser libraries are served as local fixed visualizer assets. This keeps the
 * live server and static export usable on machines without CDN access while
 * preserving the current global-library bootstrap order.
 */
const LOCAL_VENDOR_SCRIPTS = [
  {
    name: "force-graph",
    src: "vendor/force-graph.min.js",
  },
  {
    name: "marked",
    src: "vendor/marked.min.js",
  },
  {
    name: "dompurify",
    src: "vendor/purify.min.js",
  },
  {
    name: "mermaid",
    src: "vendor/mermaid.min.js",
  },
];

describe("visualizer PAGE", () => {
  test("is a full HTML document", () => {
    expect(PAGE.startsWith("<!doctype html>")).toBe(true);
    expect(PAGE).toContain("<title>OpenWiki visualizer</title>");
  });

  test("loads styles from an external stylesheet in both modes", () => {
    expect(PAGE).toContain('<link rel="stylesheet" href="/styles.css" />');
    expect(STATIC_PAGE).toContain(
      '<link rel="stylesheet" href="./styles.css" />',
    );
    expect(PAGE).not.toMatch(/<style\b/u);
    expect(STATIC_PAGE).not.toMatch(/<style\b/u);
  });

  test("loads Inter from the local font stylesheet in both modes", () => {
    expect(PAGE).toContain('<link rel="stylesheet" href="/fonts.css" />');
    expect(STATIC_PAGE).toContain(
      '<link rel="stylesheet" href="./fonts.css" />',
    );
  });

  test.each(LOCAL_VENDOR_SCRIPTS)(
    "loads $name from local visualizer vendor assets",
    ({ src }) => {
      expect(PAGE).toContain(`src="/${src}"`);
      expect(STATIC_PAGE).toContain(`src="./${src}"`);
    },
  );

  test("does not reference external CDN or font origins", () => {
    expect(PAGE).not.toContain("cdn.jsdelivr.net");
    expect(STATIC_PAGE).not.toContain("cdn.jsdelivr.net");
    expect(PAGE).not.toContain("fonts.googleapis.com");
    expect(STATIC_PAGE).not.toContain("fonts.googleapis.com");
    expect(PAGE).not.toContain("integrity=");
    expect(STATIC_PAGE).not.toContain("integrity=");
  });
});
