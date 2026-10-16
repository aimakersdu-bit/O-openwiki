#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const IGNORED_DIRS = new Set([
  '.git',
  '.openwiki',
  '_project_hub',
  'node_modules',
  'target',
  'vendor',
  'dist',
  'bin',
  'build',
  '.idea',
  '.vscode',
]);

const CODE_EXTS = new Set([
  '.java',
  '.go',
  '.ts',
  '.js',
  '.py',
  '.proto',
  '.xml',
  '.json',
  '.yaml',
  '.yml',
]);

/**
 * Scan directory recursively for code files, skipping ignored heavy dirs.
 */
function walkCodeFiles(dir, fileList = []) {
  if (!fs.existsSync(dir)) return fileList;
  const entries = fs.readdirSync(dir, { withFileTypes: true });

  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!IGNORED_DIRS.has(entry.name)) {
        walkCodeFiles(fullPath, fileList);
      }
    } else if (entry.isFile()) {
      const ext = path.extname(entry.name).toLowerCase();
      if (CODE_EXTS.has(ext)) {
        fileList.push(fullPath);
      }
    }
  }

  return fileList;
}

/**
 * Extracts provided/consumed APIs and MQ topics from code content.
 */
function analyzeCode(filePath, content, service) {
  const ext = path.extname(filePath).toLowerCase();

  // 1. Java Spring / JAX-RS
  if (ext === '.java') {
    // Spring @RequestMapping class prefix
    let classPrefix = '';
    const classMappingMatch = content.match(/@RequestMapping\s*\(\s*(?:(?:value|path)\s*=\s*)?["']([^"']+)["']/);
    if (classMappingMatch) {
      classPrefix = classMappingMatch[1].trim();
    }

    // Method mappings
    const methodRegex = /@(?:GetMapping|PostMapping|PutMapping|DeleteMapping|PatchMapping|RequestMapping)\s*\(\s*(?:(?:value|path)\s*=\s*)?["']([^"']+)["']/g;
    let m;
    while ((m = methodRegex.exec(content)) !== null) {
      let subPath = m[1].trim();
      let fullRoute = path.posix.join(classPrefix, subPath);
      if (!fullRoute.startsWith('/')) fullRoute = '/' + fullRoute;
      service.providedHttp.push({ path: fullRoute, file: filePath });
    }

    // Feign Client
    const feignRegex = /@FeignClient\s*\([^)]*?(?:(?:name|value)\s*=\s*)?["']([^"']+)["']/g;
    while ((m = feignRegex.exec(content)) !== null) {
      service.consumedRpc.push({ target: m[1].trim(), file: filePath });
    }

    // Kafka / MQ Producer
    const mqSendRegex = /(?:kafkaTemplate\.send|convertAndSend|rocketMQTemplate\.syncSend)\s*\(\s*["']([A-Za-z0-9_\-\.]+)["']/g;
    while ((m = mqSendRegex.exec(content)) !== null) {
      service.providedMq.push({ topic: m[1].trim(), file: filePath });
    }

    // Kafka / RocketMQ Consumer
    const mqListenRegex = /@(?:KafkaListener|RocketMQMessageListener)\s*\([^)]*?topics?\s*=\s*["']([A-Za-z0-9_\-\.]+)["']/g;
    while ((m = mqListenRegex.exec(content)) !== null) {
      service.consumedMq.push({ topic: m[1].trim(), file: filePath });
    }
  }

  // 2. Go (Gin, Echo, Chi, HTTP)
  if (ext === '.go') {
    const goRouteRegex = /\.(?:GET|POST|PUT|DELETE|PATCH|Any)\s*\(\s*["']([^"']+)["']/g;
    let m;
    while ((m = goRouteRegex.exec(content)) !== null) {
      let route = m[1].trim();
      if (route.startsWith('/')) {
        service.providedHttp.push({ path: route, file: filePath });
      }
    }

    // Outgoing HTTP calls
    const httpCallRegex = /(?:http\.(?:Get|Post|NewRequest)|client\.(?:Do|Get|Post))\s*\([^)]*?["'](https?:\/\/[^"']+|\/api\/[^"']+)["']/g;
    while ((m = httpCallRegex.exec(content)) !== null) {
      service.consumedHttp.push({ url: m[1].trim(), file: filePath });
    }
  }

  // 3. TypeScript / JavaScript (Express, NestJS, Axios, Fetch)
  if (ext === '.ts' || ext === '.js') {
    const tsRouteRegex = /@(?:Get|Post|Put|Delete|Patch)\s*\(\s*["']([^"']+)["']/g;
    let m;
    while ((m = tsRouteRegex.exec(content)) !== null) {
      let route = m[1].trim();
      if (!route.startsWith('/')) route = '/' + route;
      service.providedHttp.push({ path: route, file: filePath });
    }

    // Axios / Fetch calls
    const axiosRegex = /(?:axios|http|fetch)\.(?:get|post|put|delete)\s*\(\s*["']([^"']+)["']/g;
    while ((m = axiosRegex.exec(content)) !== null) {
      let callUrl = m[1].trim();
      if (callUrl.startsWith('/') || callUrl.startsWith('http')) {
        service.consumedHttp.push({ url: callUrl, file: filePath });
      }
    }
  }
}

/**
 * Main Linker entrypoint.
 */
export async function runLinker(workspaceDir = process.cwd()) {
  const ws = path.resolve(workspaceDir);
  console.log(`[Linker] Scanning virtual workspace: ${ws}`);

  const entries = fs.readdirSync(ws, { withFileTypes: true });
  const services = new Map();

  for (const entry of entries) {
    if (!entry.isDirectory() && !entry.isSymbolicLink()) continue;
    if (IGNORED_DIRS.has(entry.name)) continue;

    const serviceName = entry.name;
    const servicePath = path.join(ws, serviceName);

    const serviceData = {
      name: serviceName,
      path: servicePath,
      providedHttp: [],
      consumedHttp: [],
      consumedRpc: [],
      providedMq: [],
      consumedMq: [],
    };

    const files = walkCodeFiles(servicePath);
    for (const file of files) {
      try {
        const content = fs.readFileSync(file, 'utf-8');
        analyzeCode(file, content, serviceData);
      } catch {
        // Skip unreadable files
      }
    }

    services.set(serviceName, serviceData);
  }

  // Stitching edges
  const nodes = [];
  const edges = [];
  const edgeSet = new Set();

  for (const [name, data] of services.entries()) {
    nodes.push({
      id: name,
      name,
      providedHttpCount: data.providedHttp.length,
      consumedHttpCount: data.consumedHttp.length,
      providedMqCount: data.providedMq.length,
      consumedMqCount: data.consumedMq.length,
    });
  }

  // 1. Match MQ Topics
  for (const [srcName, srcData] of services.entries()) {
    for (const prod of srcData.providedMq) {
      for (const [destName, destData] of services.entries()) {
        if (srcName === destName) continue;
        const matchingConsumer = destData.consumedMq.find(c => c.topic === prod.topic);
        if (matchingConsumer) {
          const edgeKey = `mq:${srcName}->${destName}:${prod.topic}`;
          if (!edgeSet.has(edgeKey)) {
            edgeSet.add(edgeKey);
            edges.push({
              source: srcName,
              target: destName,
              type: 'MQ',
              label: prod.topic,
              detail: `Topic: ${prod.topic}`,
            });
          }
        }
      }
    }
  }

  // 2. Match Feign / RPC Clients
  for (const [srcName, srcData] of services.entries()) {
    for (const rpc of srcData.consumedRpc) {
      for (const [destName] of services.entries()) {
        if (srcName === destName) continue;
        if (destName.toLowerCase().includes(rpc.target.toLowerCase()) || rpc.target.toLowerCase().includes(destName.toLowerCase())) {
          const edgeKey = `rpc:${srcName}->${destName}:${rpc.target}`;
          if (!edgeSet.has(edgeKey)) {
            edgeSet.add(edgeKey);
            edges.push({
              source: srcName,
              target: destName,
              type: 'RPC_FEIGN',
              label: `@FeignClient(${rpc.target})`,
              detail: `Feign Client binding`,
            });
          }
        }
      }
    }
  }

  // 3. Match HTTP Endpoints
  for (const [srcName, srcData] of services.entries()) {
    for (const call of srcData.consumedHttp) {
      for (const [destName, destData] of services.entries()) {
        if (srcName === destName) continue;

        let matched = false;
        let matchedEndpoint = '';

        for (const prov of destData.providedHttp) {
          if (call.url.includes(prov.path) || (call.url.includes(destName) && prov.path.length > 3)) {
            matched = true;
            matchedEndpoint = prov.path;
            break;
          }
        }

        if (matched) {
          const edgeKey = `http:${srcName}->${destName}:${matchedEndpoint}`;
          if (!edgeSet.has(edgeKey)) {
            edgeSet.add(edgeKey);
            edges.push({
              source: srcName,
              target: destName,
              type: 'HTTP',
              label: `HTTP ${matchedEndpoint}`,
              detail: `Endpoint: ${matchedEndpoint}`,
            });
          }
        }
      }
    }
  }

  // Persist outputs
  const hubDir = path.join(ws, '_project_hub');
  const openwikiDir = path.join(hubDir, 'openwiki', 'architecture');
  const contractsDir = path.join(hubDir, 'contracts');

  fs.mkdirSync(openwikiDir, { recursive: true });
  fs.mkdirSync(contractsDir, { recursive: true });

  const graphData = {
    generated_at: new Date().toISOString(),
    nodes,
    edges,
  };

  fs.writeFileSync(path.join(hubDir, 'cross_repo_graph.json'), JSON.stringify(graphData, null, 2), 'utf-8');

  // Generate Mermaid Topology Markdown
  let mermaid = '```mermaid\ngraph TD\n';
  for (const node of nodes) {
    mermaid += `    ${node.id}["${node.name}"]\n`;
  }
  for (const edge of edges) {
    const cleanLabel = edge.label.replace(/"/g, "'");
    mermaid += `    ${edge.source} -->|"${cleanLabel}"| ${edge.target}\n`;
  }
  mermaid += '```\n';

  const topologyDoc = `# 微服务调用与事件依赖拓扑 (Service Topology)

本架构拓扑由 OpenWiki 跨仓契约缝合器（Linker）自动扫描并分析生成。

## 1. 全局服务交互图

${mermaid}

## 2. 微服务组件清单

| 微服务组件 | 提供 HTTP 端点数 | 消费 HTTP 接口数 | 生产 MQ Topic 数 | 消费 MQ Topic 数 |
| :--- | :--- | :--- | :--- | :--- |
${nodes.map(n => `| **${n.name}** | ${n.providedHttpCount} | ${n.consumedHttpCount} | ${n.providedMqCount} | ${n.consumedMqCount} |`).join('\n')}

## 3. 跨服务调用契约明细

${edges.length === 0 ? '_未检测到显式跨服务调用链路_' : edges.map((e, idx) => `${idx + 1}. **${e.source}** ➔ **${e.target}** [${e.type}]: \`${e.label}\``).join('\n')}
`;

  fs.writeFileSync(path.join(openwikiDir, 'topology.md'), topologyDoc, 'utf-8');

  // Contracts output
  const httpApis = [];
  const mqTopics = [];
  for (const [name, data] of services.entries()) {
    for (const p of data.providedHttp) {
      httpApis.push({ service: name, path: p.path, file: path.relative(ws, p.file) });
    }
    for (const p of data.providedMq) {
      mqTopics.push({ service: name, role: 'producer', topic: p.topic, file: path.relative(ws, p.file) });
    }
    for (const c of data.consumedMq) {
      mqTopics.push({ service: name, role: 'consumer', topic: c.topic, file: path.relative(ws, c.file) });
    }
  }

  fs.writeFileSync(path.join(contractsDir, 'http_apis.json'), JSON.stringify(httpApis, null, 2), 'utf-8');
  fs.writeFileSync(path.join(contractsDir, 'mq_topics.json'), JSON.stringify(mqTopics, null, 2), 'utf-8');

  console.log(`[Linker] Finished successfully. Identified ${nodes.length} services and ${edges.length} cross-repo edges.`);
  return graphData;
}

// CLI runner
if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  const targetDir = process.argv[2] || process.cwd();
  runLinker(targetDir).catch((err) => {
    console.error('[Linker] Fatal error:', err);
    process.exit(1);
  });
}
