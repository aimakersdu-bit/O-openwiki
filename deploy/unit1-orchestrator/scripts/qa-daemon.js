#!/usr/bin/env node

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// 1. Parse command-line flags
const args = process.argv.slice(2);
let socketPath = '';
let ttlSeconds = 7200; // default 2 hours
let repoDir = process.cwd();

for (const arg of args) {
  if (arg.startsWith('--socket=')) {
    socketPath = arg.substring('--socket='.length);
  } else if (arg.startsWith('--ttl=')) {
    ttlSeconds = parseInt(arg.substring('--ttl='.length), 10) || 7200;
  } else if (arg.startsWith('--repo-dir=')) {
    repoDir = path.resolve(arg.substring('--repo-dir='.length));
  }
}

if (!socketPath && process.env.OPENWIKI_QA_SOCKET) {
  socketPath = process.env.OPENWIKI_QA_SOCKET;
}

if (!socketPath) {
  console.error('[QA-Daemon] Error: --socket=<path> is required.');
  process.exit(1);
}

// Ensure working directory is set
try {
  process.chdir(repoDir);
} catch (err) {
  console.error(`[QA-Daemon] Failed to change cwd to ${repoDir}:`, err);
}

// 2. Dynamically import runOpenWikiAgent
const __dirname = path.dirname(fileURLToPath(import.meta.url));
let agentModulePath = '';

const candidates = [
  process.env.OPENWIKI_DIST_DIR ? path.join(process.env.OPENWIKI_DIST_DIR, 'agent', 'index.js') : '',
  path.resolve(__dirname, '../../../dist/agent/index.js'),
  path.resolve(repoDir, 'node_modules/openwiki/dist/agent/index.js'),
  path.resolve('/app/dist/agent/index.js')
].filter(Boolean);

for (const candidate of candidates) {
  if (fs.existsSync(candidate)) {
    agentModulePath = candidate;
    break;
  }
}

if (!agentModulePath) {
  console.error('[QA-Daemon] Error: Could not locate openwiki dist/agent/index.js. Checked:', candidates);
  process.exit(1);
}

console.log(`[QA-Daemon] Loading agent from: ${agentModulePath}`);
const { runOpenWikiAgent } = await import(agentModulePath);

// 3. FIFO Mutex Queue to prevent concurrent state pollution (global fetch & sqlite lock)
class FifoQueue {
  constructor() {
    this.chain = Promise.resolve();
  }

  enqueue(task) {
    const p = new Promise((resolve, reject) => {
      this.chain = this.chain.finally(async () => {
        try {
          const res = await task();
          resolve(res);
        } catch (err) {
          reject(err);
        }
      });
    });
    return p;
  }
}

const queue = new FifoQueue();

// 4. Idle TTL Lifecycle
let lastAccessedAt = Date.now();
let activeRequests = 0;
const ttlMs = ttlSeconds * 1000;

const idleTimer = setInterval(() => {
  if (activeRequests === 0 && Date.now() - lastAccessedAt >= ttlMs) {
    console.log(`[QA-Daemon] Idle timeout of ${ttlSeconds}s reached. Shutting down gracefully...`);
    clearInterval(idleTimer);
    cleanupAndExit(0);
  }
}, 5000);
idleTimer.unref();

function touch() {
  lastAccessedAt = Date.now();
}

function cleanupAndExit(code = 0) {
  try {
    server.close(() => {
      try {
        if (socketPath && fs.existsSync(socketPath)) {
          fs.unlinkSync(socketPath);
        }
      } catch {}
      process.exit(code);
    });
  } catch {
    process.exit(code);
  }
}

// 5. HTTP Server over Unix Domain Socket
const server = http.createServer(async (req, res) => {
  touch();
  console.log(`[QA-Daemon] Request: ${req.method} ${req.url}`);

  if (req.method === 'GET' && req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'ok', repoDir, activeRequests }));
    return;
  }

  if (req.method !== 'POST' || (req.url !== '/chat' && req.url !== '/')) {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not found' }));
    return;
  }

  // Read request body
  let body = '';
  req.on('data', (chunk) => {
    body += chunk;
  });

  req.on('end', async () => {
    let payload;
    try {
      payload = JSON.parse(body);
    } catch (err) {
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'Invalid JSON: ' + err.message }));
      return;
    }

    const { question, user_id, session_id, thread_id } = payload;
    if (!question) {
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'Missing "question" in payload' }));
      return;
    }

    activeRequests++;
    touch();

    // Prepare SSE Headers
    res.writeHead(200, {
      'Content-Type': 'text/event-stream; charset=utf-8',
      'Cache-Control': 'no-cache, no-transform',
      'Connection': 'keep-alive',
      'X-Accel-Buffering': 'no'
    });

    const sendSSE = (event, data) => {
      try {
        res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
      } catch {}
    };

    // Client disconnect handling
    let clientDisconnected = false;
    res.on('close', () => {
      if (!res.writableEnded) {
        clientDisconnected = true;
      }
    });

    try {
      // Execute within FIFO queue to ensure only 1 run per repo worker at any time
      await queue.enqueue(async () => {
        if (clientDisconnected) return;

        console.log(`[QA-Daemon] Starting agent run for question: "${question}" in ${repoDir}`);
        sendSSE('status', { stage: 'start', message: 'Agent initialized' });

        const effectiveThreadId = thread_id || session_id || undefined;
        let fullAnswer = '';

        try {
          await runOpenWikiAgent(
            'chat',
            repoDir,
            {
              outputMode: 'repository',
              userMessage: question,
              threadId: effectiveThreadId,
              onEvent: (event) => {
                if (clientDisconnected) return;
                touch();

                if (event.type === 'text') {
                  fullAnswer += event.text;
                  sendSSE('delta', { text: event.text });
                } else if (event.type === 'tool_start') {
                  sendSSE('status', {
                    stage: 'tool_start',
                    name: event.name,
                    call: event.call
                  });
                } else if (event.type === 'tool_end') {
                  sendSSE('status', {
                    stage: 'tool_end',
                    name: event.name,
                    status: event.status
                  });
                }
              }
            }
          );

          if (!clientDisconnected) {
            sendSSE('done', {
              fullAnswer,
              threadId: effectiveThreadId
            });
            res.end();
          }
        } catch (runErr) {
          console.error('[QA-Daemon] Agent execution error:', runErr);
          if (!clientDisconnected) {
            sendSSE('error', {
              error: runErr.message || String(runErr)
            });
            res.end();
          }
        }
      });
    } catch (queueErr) {
      console.error('[QA-Daemon] Queue execution error:', queueErr);
      if (!clientDisconnected) {
        sendSSE('error', { error: queueErr.message || String(queueErr) });
        res.end();
      }
    } finally {
      activeRequests--;
      touch();
    }
  });
});

// Remove existing socket file if present
try {
  if (fs.existsSync(socketPath)) {
    fs.unlinkSync(socketPath);
  }
} catch (err) {
  console.warn(`[QA-Daemon] Notice: could not remove previous socket ${socketPath}:`, err.message);
}

// Handle signals
process.on('SIGINT', () => cleanupAndExit(0));
process.on('SIGTERM', () => cleanupAndExit(0));

server.listen(socketPath, () => {
  console.log(`[QA-Daemon] Worker listening on UDS ${socketPath} (Repo: ${repoDir}, TTL: ${ttlSeconds}s)`);
});
