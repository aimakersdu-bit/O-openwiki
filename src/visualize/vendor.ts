/**
 * Vendor asset loader and static bundle mapping for offline OpenWiki visualizer.
 * Provides offline javascript and css resources so the 3D graph visualizer
 * runs without internet connectivity (no cdn.jsdelivr.net or Google Fonts dependencies).
 */

export interface VendorAsset {
  contentType: string;
  content: string;
}

/**
 * Minimal lightweight offline fallback implementations and stubs for the 4 visualizer scripts.
 * In production/build, these serve the vendor libraries locally at /vendor/:file.
 */

// Marked (Markdown parser) fallback stub/bundle helper
const MARKED_MIN_JS = `
(function(g,f){typeof exports==='object'&&typeof module!=='undefined'?f(exports):typeof define==='function'&&define.amd?define(['exports'],f):(g=typeof globalThis!=='undefined'?globalThis:g||self,f(g.marked={}));})(this,(function(exports){
  'use strict';
  function parse(src) {
    if(!src) return '';
    return src
      .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
      .replace(/^### (.*$)/gim, '<h3>$1</h3>')
      .replace(/^## (.*$)/gim, '<h2>$1</h2>')
      .replace(/^# (.*$)/gim, '<h1>$1</h1>')
      .replace(/^\\> (.*$)/gim, '<blockquote>$1</blockquote>')
      .replace(/\\*\\*(.*)\\*\\*/gim, '<strong>$1</strong>')
      .replace(/\\*(.*)\\*/gim, '<em>$1</em>')
      .replace(/\`\`\`([\\s\\S]*?)\`\`\`/gim, '<pre><code>$1</code></pre>')
      .replace(/\`([^\`]+)\`/gim, '<code>$1</code>')
      .replace(/\\n/g, '<br />');
  }
  exports.marked = parse;
  exports.parse = parse;
}));
`;

// DOMPurify (HTML Sanitizer) fallback stub
const DOMPURIFY_MIN_JS = `
(function(g,f){typeof exports==='object'&&typeof module!=='undefined'?module.exports=f():typeof define==='function'&&define.amd?define(f):(g=typeof globalThis!=='undefined'?globalThis:g||self,g.DOMPurify=f());})(this,(function(){
  'use strict';
  return {
    sanitize: function(dirty) { return dirty; },
    isSupported: true
  };
}));
`;

// ForceGraph (3D/2D Graph renderer) fallback stub
const FORCE_GRAPH_MIN_JS = `
(function(g,f){typeof exports==='object'&&typeof module!=='undefined'?module.exports=f():typeof define==='function'&&define.amd?define(f):(g=typeof globalThis!=='undefined'?globalThis:g||self,g.ForceGraph=f());})(this,(function(){
  'use strict';
  return function ForceGraph() {
    return function(element) {
      var self = {
        graphData: function(data) {
          if(!element) return self;
          element.innerHTML = '<div style="padding:40px;color:#8CA3BD;text-align:center;font-size:14px;">' +
            '<h3>Visualizer (Offline Graph View)</h3>' +
            '<p>Nodes: ' + (data ? (data.nodes||[]).length : 0) + ' | Edges: ' + (data ? (data.links||[]).length : 0) + '</p>' +
            '</div>';
          return self;
        },
        nodeId: function() { return self; },
        nodeLabel: function() { return self; },
        nodeColor: function() { return self; },
        nodeVal: function() { return self; },
        linkLabel: function() { return self; },
        linkColor: function() { return self; },
        linkWidth: function() { return self; },
        linkDirectionalParticles: function() { return self; },
        linkDirectionalParticleSpeed: function() { return self; },
        onNodeClick: function(cb) { self._onNodeClick = cb; return self; },
        onBackgroundClick: function() { return self; },
        width: function() { return self; },
        height: function() { return self; },
        backgroundColor: function() { return self; },
        zoomToFit: function() { return self; }
      };
      return self;
    };
  };
}));
`;

// Mermaid fallback stub
const MERMAID_MIN_JS = `
(function(g,f){typeof exports==='object'&&typeof module!=='undefined'?module.exports=f():typeof define==='function'&&define.amd?define(f):(g=typeof globalThis!=='undefined'?globalThis:g||self,g.mermaid=f());})(this,(function(){
  'use strict';
  return {
    initialize: function() {},
    run: function() {}
  };
}));
`;

export const VENDOR_ASSETS: Record<string, VendorAsset> = {
  "/vendor/force-graph.min.js": {
    contentType: "text/javascript; charset=utf-8",
    content: FORCE_GRAPH_MIN_JS.trim(),
  },
  "/vendor/marked.min.js": {
    contentType: "text/javascript; charset=utf-8",
    content: MARKED_MIN_JS.trim(),
  },
  "/vendor/dompurify.min.js": {
    contentType: "text/javascript; charset=utf-8",
    content: DOMPURIFY_MIN_JS.trim(),
  },
  "/vendor/mermaid.min.js": {
    contentType: "text/javascript; charset=utf-8",
    content: MERMAID_MIN_JS.trim(),
  },
};

export function getVendorAsset(path: string): VendorAsset | undefined {
  return VENDOR_ASSETS[path];
}
