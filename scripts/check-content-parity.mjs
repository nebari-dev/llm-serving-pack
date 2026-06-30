#!/usr/bin/env node
// Verifies each migrated Starlight page body is byte-identical to its Hugo source.
// Hugo source is read from git ref so the check survives docs/site removal.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const REF = process.env.HUGO_REF || 'origin/docs-site';
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const docsDir = path.join(repoRoot, 'docs/astro/src/content/docs');

// migrated file (in src/content/docs) -> hugo source basename (in docs/site/content)
const MAP = {
  'index.md': '_index.md',
  'quickstart.md': 'quickstart.md',
  'installation.md': 'installation.md',
  'local-development.md': 'local-development.md',
  'shared-storage.md': 'shared-storage.md',
  'troubleshooting.md': 'troubleshooting.md',
  'configuration.md': 'configuration.md',
  'architecture.md': 'architecture.md',
  'cicd-and-releasing.md': 'cicd-and-releasing.md',
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
