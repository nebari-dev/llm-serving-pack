import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parsePins } from '../src/plugins/remark-pins';

const docsDir = fileURLToPath(new URL('..', import.meta.url));
const contentDir = join(docsDir, 'src/content/docs');
const pins = parsePins(readFileSync(join(docsDir, '..', 'versions.env'), 'utf8'));

// Lines that keep a pin value on purpose. Identified by a stable substring of
// the line, not a line number, so edits elsewhere in the file do not break this.
//  - upgrading.mdx: the historical AI Gateway 0.5 procedure and the upstream
//    permalink are frozen at the release they describe.
const ALLOW: Array<{ file: string; contains: string }> = [
  { file: 'upgrading.mdx', contains: 'ai-gateway/blob/v1.1.0/RELEASES.md' },
  { file: 'upgrading.mdx', contains: 'ai-gateway/blob/v1.1.0/internal/controller/gateway.go' },
];

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(join(dir, e.name)) : /\.mdx?$/.test(e.name) ? [join(dir, e.name)] : [],
  );
}

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// Each pin value, plus its v-less form for v-prefixed values (chart versions
// and prose such as "Envoy Gateway 1.8.1"). Digit boundaries stop 1.5.1
// matching inside 11.5.10 or 1.5.1.2.
const needles = Object.entries(pins).flatMap(([key, value]) => {
  const forms = value.startsWith('v') ? [value, value.slice(1)] : [value];
  return forms.map((f) => ({ key, text: f, re: new RegExp(`(?<![\\d.])${escapeRe(f)}(?!\\d|\\.\\d)`) }));
});

describe('docs pins', () => {
  it('no untokenised pin values', () => {
    const hits: string[] = [];
    for (const file of walk(contentDir)) {
      const rel = relative(contentDir, file);
      readFileSync(file, 'utf8').split('\n').forEach((line, i) => {
        if (ALLOW.some((a) => a.file === rel && line.includes(a.contains))) return;
        const seen = new Set<string>();
        for (const n of needles) {
          if (seen.has(n.key) || !n.re.test(line)) continue;
          seen.add(n.key);
          hits.push(`${rel}:${i + 1}: ${n.text} (use %%pin:${n.key}%%)`);
        }
      });
    }
    expect(hits, `pin values written as literals:\n${hits.join('\n')}`).toEqual([]);
  });
});
