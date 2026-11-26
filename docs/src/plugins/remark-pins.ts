// Replaces %%pin:KEY%% tokens with values from versions.env, so the docs never
// carry a pin as a literal. Substitutes in text, inlineCode, code and link.url.
// A token naming an unknown key throws. A token left in any other node (image
// url, html, ...) throws, so it cannot silently ship.
import { visit } from 'unist-util-visit';
import type { Root } from 'mdast';

const TOKEN = /%%pin:([A-Za-z0-9_]+)%%/g;

// Mirrors ParsePins in operator/cmd/render-pins/pins.go: KEY=value per line,
// # comments and blank lines ignored, no quoting, no spaces around "=", no $
// expansion, no duplicate keys. Errors name the line.
export function parsePins(text: string): Record<string, string> {
  const pins: Record<string, string> = {};
  const lines = text.split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  lines.forEach((raw, i) => {
    const n = i + 1;
    const line = raw.replace(/\r$/, '');
    const trimmed = line.trim();
    if (trimmed === '' || trimmed.startsWith('#')) return;
    const eq = line.indexOf('=');
    if (eq < 0) throw new Error(`line ${n}: want KEY=value, got "${line}"`);
    const key = line.slice(0, eq);
    const value = line.slice(eq + 1);
    if (key === '') throw new Error(`line ${n}: empty key`);
    if (key !== key.trim() || /[ \t]/.test(key)) {
      throw new Error(`line ${n}: no spaces allowed in key "${key}"`);
    }
    if (value !== value.trim()) {
      throw new Error(`line ${n}: no spaces allowed around '=' for ${key}`);
    }
    if (/["']/.test(value)) throw new Error(`line ${n}: quoting is not allowed (${key})`);
    if (value.includes('$')) throw new Error(`line ${n}: '$' expansion is not allowed (${key})`);
    if (Object.prototype.hasOwnProperty.call(pins, key)) {
      throw new Error(`line ${n}: duplicate key ${key}`);
    }
    pins[key] = value;
  });
  return pins;
}

export default function remarkPins(options: { pins: Record<string, string> }) {
  const pins = options?.pins ?? {};
  const sub = (s: string): string =>
    s.replace(TOKEN, (_m, key: string) => {
      if (!Object.prototype.hasOwnProperty.call(pins, key)) {
        throw new Error(`remark-pins: unknown pin key ${key} (not in versions.env)`);
      }
      return pins[key];
    });

  // The build's markdown pipeline splits one run of text around a token: the
  // directive syntax Starlight enables reads ":KEY" in "%%pin:KEY%%" as a text
  // directive, and other extensions may split text too. Fold a bare ":KEY"
  // directive that follows "%%pin" back into text, then join adjacent text
  // nodes, so a token is always inside a single text node.
  const mergeAdjacentText = (tree: Root) => {
    visit(tree, (node: any) => {
      if (!Array.isArray(node.children)) return;
      const merged: any[] = [];
      for (const child of node.children) {
        const prev = merged[merged.length - 1];
        const bareDirective =
          child.type === 'textDirective' &&
          !child.children?.length &&
          Object.keys(child.attributes ?? {}).length === 0 &&
          prev?.type === 'text' &&
          prev.value.endsWith('%%pin');
        if (bareDirective) prev.value += `:${child.name}`;
        else if (child.type === 'text' && prev?.type === 'text') prev.value += child.value;
        else merged.push(child);
      }
      node.children = merged;
    });
  };

  return (tree: Root) => {
    mergeAdjacentText(tree);
    visit(tree, ['text', 'inlineCode', 'code', 'link'], (node: any) => {
      const field = node.type === 'link' ? 'url' : 'value';
      if (typeof node[field] === 'string') node[field] = sub(node[field]);
    });
    // Anything still carrying a token is in a node type we do not substitute.
    visit(tree, (node: any) => {
      const { children: _c, position: _p, ...own } = node;
      if (JSON.stringify(own).includes('%%pin:')) {
        throw new Error(`remark-pins: %%pin:...%% token left in a ${node.type} node (only text, inlineCode, code and link.url are substituted)`);
      }
    });
  };
}
