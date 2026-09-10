import {
  cp,
  mkdir,
  mkdtemp,
  readdir,
  rename,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import {
  ensureOpenWikiHome,
  openWikiSkillsDir,
} from "../config/openwiki-home.js";

const bundledSkillsDir = fileURLToPath(
  new URL("../../skills", import.meta.url),
);

const EMBEDDED_SKILLS: Record<string, Record<string, string>> = {
  "mermaid-diagrams": {
    "SKILL.md": `---
name: mermaid-diagrams
description: Embed Mermaid diagrams in generated wiki pages. Use whenever documenting a runtime or request flow, a call sequence, a state machine or lifecycle, a data model or entity relationships, or non-trivial control flow, since these are clearer as a diagram than as prose. Also use when an update run touches a page that already contains a mermaid fence, or a page that contains a text fence a previous run degraded.
---

# Mermaid Diagrams In Generated Wiki Pages

Diagrams are part of high-quality wiki generation, not decoration. Where a flow,
lifecycle, or data model is easier to grasp visually, embed a Mermaid diagram in
a fenced \`\`\`mermaid block on the most relevant page.

## Choosing a diagram type

- \`sequenceDiagram\` for runtime and request flows across components (auth flows, request lifecycles, agent tool loops).
- \`stateDiagram-v2\` for lifecycles and state machines (job states, connection states, run phases).
- \`erDiagram\` for the data model: entities and their relationships.
- \`flowchart TD\` for branching control flow and decision logic.

## Discipline

- Ground every diagram in inspected source. Do not invent participants, states, entities, or relationships the code does not support.
- Cover the high-value cases: add a diagram wherever a page documents a request or runtime flow, a call sequence, a lifecycle or state machine, or a data model. A repository wiki usually has several such diagrams, not one overall. Skip pages that are navigation, reference tables, or pure configuration.
- Still prefer a few strong diagrams over decorating every page: one accurate diagram on the page that needs it beats a diagram forced onto every page.
- Give each diagram a one-line caption directly below it stating what it shows.
- OpenWiki validates every mermaid fence after your run and converts fences that fail to parse into plain text fences. A degraded diagram is a quality failure; follow the syntax rules below so it does not happen.

## Syntax safety

These rules prevent the most common render breakages. When in doubt, rephrase the label.

- Never place semicolons or pipes inside node, message, or edge labels.
- Never place unescaped angle brackets in labels; write "returns Promise of User" instead of "returns Promise<User>".
- In \`flowchart\`, wrap any label containing parentheses, brackets, or other punctuation in double quotes: \`A["calls foo(bar)"]\`.
- In \`flowchart\`, never use the bare word \`end\` as a node id, and never start a node id with \`o\` or \`x\` followed by a dash (both are edge-marker syntax); rename the node.
- In \`sequenceDiagram\`, participant names with spaces or punctuation need an alias: \`participant AS as Auth Service\`.
- Never use a Mermaid reserved word as a participant name, alias, or node id: \`note\`, \`end\`, \`loop\`, \`alt\`, \`opt\`, \`par\`, \`and\`, \`else\`, \`activate\`, \`deactivate\`, \`class\`, \`state\`, \`click\`, \`link\`. For example a notification participant must be \`Notifier\`, not \`Note\` (which collides with the \`note\` keyword).
- In \`erDiagram\`, entity and attribute names must be single identifier-like tokens; put human phrasing in the relationship label.
- Keep labels short. Move explanation into the surrounding prose or the caption, not the diagram.

## Update runs

- A wrong diagram is a stale claim, not existing structure to preserve. If a source change makes a diagram inaccurate, update the diagram in the same edit as the surrounding prose.
- Do not rewrite a diagram that is still accurate. Regenerating unchanged diagrams creates diff noise.
- If a page contains a text fence preceded by an HTML comment starting with "openwiki: mermaid parse failed", that is a diagram a previous run degraded. Fix the syntax using the parser error in the comment, restore the \`\`\`mermaid fence, and delete the comment.
`,
  },
  "write-connector": {
    "SKILL.md": `---
name: write-connector
description: Add a new built-in OpenWiki source connector. Use when a user asks to create or implement an OpenWiki connector.
---

# Write An OpenWiki Connector

OpenWiki connectors are built-in TypeScript modules in the OSS repository. Do not create a plugin marketplace, dynamic connector package, or runtime-loaded untrusted connector. Add normal source files and tests.

## Required Shape

- Add the connector to src/connectors/types.ts and src/connectors/registry.ts.
- Implement the connector under src/connectors/sources/<connector>.ts.
- The connector must expose a ConnectorRuntime with id, displayName, description, backend, requiredEnv, supportsAgenticDiscovery, and ingest().
- Ingestion writes raw JSON/manifests under ~/.openwiki/connectors/<id>/raw/<run-id>/.
- State lives in ~/.openwiki/connectors/<id>/state.json.
- Config lives in ~/.openwiki/connectors/<id>/state.json.
- Secrets live in ~/.openwiki/.env and are referenced only by env var name.

## Security Rules

- Never read, print, log, return, or hardcode secret values.
- Do not store credentials in connector config, raw files, state, logs, or tests.
- Validate connector IDs and raw file paths so reads and writes stay inside ~/.openwiki/connectors/<id>/.
- Use deterministic ingestion code for credentialed external fetching.
- If wrapping MCP, treat the MCP server as read-only and call only allowlisted read/dump operations from connector config.
- Do not let untrusted connector manifests instantiate arbitrary commands or arbitrary network endpoints without explicit built-in code review.

## Ingestion Rules

- Git/local repos should write compact manifests and let the agent inspect the local repo as the source of truth.
- Sources with timestamps should store per-stream cursors.
- Sources with object metadata should store IDs, last edited timestamps, and content hashes.
- Sources with pagination should store enough state to continue without refetching everything.
- Raw dumps should preserve source IDs, timestamps, URLs, authors, and enough provenance for citations.

## User-Facing Finish

When done, tell the user:

- which connector files changed,
- which env vars to set in ~/.openwiki/.env,
- what config file to create or edit,
- how to run openwiki personal --update to trigger ingestion,
- which scopes/permissions the source provider requires.
`,
  },
};

async function writeEmbeddedSkills(targetDir: string): Promise<void> {
  await mkdir(targetDir, { recursive: true });
  for (const [skillName, files] of Object.entries(EMBEDDED_SKILLS)) {
    const dirPath = path.join(targetDir, skillName);
    await mkdir(dirPath, { recursive: true });
    for (const [fileName, content] of Object.entries(files)) {
      await writeFile(path.join(dirPath, fileName), content, "utf8");
    }
  }
}

/** Copies bundled skills into the OpenWiki home while preserving other skills. */
export async function syncBundledSkills(): Promise<void> {
  await ensureOpenWikiHome();
  await replaceSkillDirectories(bundledSkillsDir, openWikiSkillsDir);
}

/** Replaces bundled skill directories without removing unrelated skills. */
export async function replaceSkillDirectories(
  sourceDir: string,
  targetDir: string,
): Promise<void> {
  try {
    const skills = (await readdir(sourceDir, { withFileTypes: true })).filter(
      (entry) => entry.isDirectory(),
    );

    await mkdir(targetDir, { recursive: true });

    await Promise.all(
      skills.map(({ name }) => replaceSkillDirectory(sourceDir, targetDir, name)),
    );
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      await writeEmbeddedSkills(targetDir);
      return;
    }
    throw error;
  }
}

/**
 * Installs a single bundled skill by staging it in a unique scratch directory
 * and atomically swapping it into place. Copying straight onto the target is
 * not idempotent: when the destination shows up mid-copy (for example when
 * two syncs overlap during `--init`), `cp`'s internal `mkdir` throws EEXIST
 * (#499). Deleting the old copy in place is racy for the same reason, so the
 * old copy is displaced with an atomic `rename` instead.
 */
async function replaceSkillDirectory(
  sourceDir: string,
  targetDir: string,
  name: string,
): Promise<void> {
  const target = path.join(targetDir, name);
  const scratchDir = await mkdtemp(path.join(targetDir, `.${name}-staging-`));
  const staged = path.join(scratchDir, "staged");
  const displaced = path.join(scratchDir, "displaced");

  try {
    await cp(path.join(sourceDir, name), staged, { recursive: true });

    // Move any existing copy aside; it is deleted with the scratch directory.
    try {
      await rename(target, displaced);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        throw error;
      }
    }

    let installError: Error | undefined;

    for (let attempt = 0; attempt < 3; attempt += 1) {
      try {
        await rename(staged, target);
        installError = undefined;
        break;
      } catch (error) {
        installError = error as Error;

        // A concurrent sync may have installed the same bundled skill first
        // (rename then fails with EEXIST/ENOTEMPTY on POSIX or EPERM on
        // Windows even though the target is complete), or displaced our
        // fresh install right before this attempt. Accept the former and
        // retry the latter.
        const installed = await stat(target).then(
          (stats) => stats.isDirectory(),
          () => false,
        );

        if (installed) {
          installError = undefined;
          break;
        }
      }
    }

    if (installError !== undefined) {
      throw installError;
    }
  } finally {
    await rm(scratchDir, { force: true, recursive: true });
  }
}
