// docs/astro/src/loaders/hugo-content.ts
//
// Reads from docs/site/content/, which is the shared source of truth with the Hugo site.
// Only `title` is consumed from TOML frontmatter; all other Hugo-only keys (weight, draft,
// menu, date, etc.) are intentionally ignored so they cannot break Starlight's strict schema.
import type { Loader } from 'astro/loaders';
import { parse as parseToml } from 'smol-toml';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { rewriteLinks } from './rewrite-links';

interface Options {
  /** Directory of Hugo markdown, relative to the Astro project root. */
  dir: string;
}

/** Split a Hugo `+++ ... +++` TOML frontmatter block from the body. */
function splitTomlFrontmatter(raw: string): { fm: Record<string, unknown>; body: string } {
  const match = raw.match(/^\+\+\+\s*\r?\n([\s\S]*?)\r?\n\+\+\+\s*\r?\n?([\s\S]*)$/);
  if (!match) return { fm: {}, body: raw };
  return { fm: parseToml(match[1]) as Record<string, unknown>, body: match[2] };
}

export function hugoContentLoader(options: Options): Loader {
  return {
    name: 'hugo-content-loader',
    async load({ store, parseData, renderMarkdown, generateDigest, config, logger }) {
      const base = (config.base || '/').endsWith('/') ? config.base : `${config.base}/`;
      const dir = path.join(fileURLToPath(config.root), options.dir);
      const files = (await fs.readdir(dir)).filter((f) => f.endsWith('.md'));
      store.clear();
      for (const file of files) {
        try {
          const filePath = path.join(dir, file);
          const raw = await fs.readFile(filePath, 'utf-8');
          const { fm, body } = splitTomlFrontmatter(raw);
          const id = file === '_index.md' ? 'index' : file.replace(/\.md$/, '');
          const rewritten = rewriteLinks(body, base);
          const data = await parseData({ id, data: { title: fm.title ?? id } });
          const rendered = await renderMarkdown(rewritten, {
            fileURL: pathToFileURL(filePath),
          });
          store.set({
            id,
            data,
            body: rewritten,
            filePath: path.relative(fileURLToPath(config.root), filePath),
            rendered,
            digest: generateDigest(rewritten),
          });
        } catch (err) {
          logger.warn(`hugo-content-loader: skipping ${file} - ${(err as Error).message}`);
        }
      }
      logger.info(`Loaded ${files.length} Hugo content files from ${options.dir}`);
    },
  };
}
