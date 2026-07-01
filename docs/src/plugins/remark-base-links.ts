// Prefixes the Astro `base` path onto root-absolute markdown links and images.
// No-op when base is '/'. Leaves external (has scheme), protocol-relative ('//'),
// anchor-only ('#...'), and already-prefixed URLs untouched. Idempotent.
import { visit } from 'unist-util-visit';
import type { Root } from 'mdast';

export function prefixUrl(url: string, base: string): string {
  if (!base || base === '/') return url;
  if (!url.startsWith('/')) return url; // anchors, relative, external (scheme)
  if (url.startsWith('//')) return url; // protocol-relative
  const prefix = base.replace(/\/$/, '');
  if (url.startsWith(`${prefix}/`)) return url; // already prefixed
  return `${prefix}${url}`;
}

export default function remarkBaseLinks(options: { base: string }) {
  const base = options?.base ?? '/';
  return (tree: Root) => {
    visit(tree, ['link', 'image'], (node: any) => {
      if (typeof node.url === 'string') {
        node.url = prefixUrl(node.url, base);
      }
    });
  };
}
