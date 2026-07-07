#!/usr/bin/env node
// Verifies each migrated Starlight page body is byte-identical to its Hugo source.
// Hugo source is read from git ref so the check survives docs/site removal.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const REF = process.env.HUGO_REF || 'origin/docs-site';
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const docsDir = path.join(repoRoot, 'docs/src/content/docs');

// Four files are intentionally excluded from this parity check:
//   - cicd-and-releasing.md documents the Astro docs workflow (not byte-identical to
//     the Hugo source by design, user-approved).
//   - installation.md has its screenshot references rewritten from /public absolute
//     paths to Astro src/assets imports (PR #120 review), so its body diverges from
//     the frozen Hugo source by design.
//   - local-development.md was updated by PR #115 to document the AI Gateway v0.5
//     local dev flow (real ext_proc, passthrough inference, key-manager UI dev mode),
//     so its body intentionally diverges from the frozen Hugo source.
//   - architecture.md was updated by PR #117 (#116) to document the now-enabled
//     per-model API-key authorization on the external endpoint, so its body
//     intentionally diverges from the frozen Hugo source.

// migrated file (in src/content/docs) -> hugo source basename (in docs/site/content)
const MAP = {
  'index.md': '_index.md',
  'quickstart.md': 'quickstart.md',
  'shared-storage.md': 'shared-storage.md',
  'troubleshooting.md': 'troubleshooting.md',
  'configuration.md': 'configuration.md',
};

// Strip a leading TOML (+++) or YAML (---) frontmatter block; return the body.
function stripFrontmatter(raw) {
  const toml = raw.match(/^\+\+\+\s*\r?\n[\s\S]*?\r?\n\+\+\+\s*\r?\n?([\s\S]*)$/);
  if (toml) return toml[1];
  const yaml = raw.match(/^---\s*\r?\n[\s\S]*?\r?\n---\s*\r?\n?([\s\S]*)$/);
  if (yaml) return yaml[1];
  return raw;
}

let failed = 0;
for (const [migrated, hugo] of Object.entries(MAP)) {
  const migratedRaw = readFileSync(path.join(docsDir, migrated), 'utf8');
  const hugoRaw = execFileSync('git', ['show', `${REF}:docs/site/content/${hugo}`], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  const a = stripFrontmatter(migratedRaw);
  const b = stripFrontmatter(hugoRaw);
  if (a !== b) {
    failed++;
    console.error(`BODY MISMATCH: ${migrated} differs from ${hugo}`);
  }
}
if (failed) {
  console.error(`\n${failed} file(s) failed content parity.`);
  process.exit(1);
}
console.log('CONTENT_PARITY_OK');
