import { describe, it, expect } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const docs = (name) =>
  fileURLToPath(new URL(`../src/content/docs/${name}`, import.meta.url));
const example = (name) =>
  fileURLToPath(new URL(`../../examples/${name}`, import.meta.url));

export const readPage = (name) => readFileSync(docs(name), 'utf8');
export const readExample = (name) => readFileSync(example(name), 'utf8');

describe('architecture: PassthroughModel embed', () => {
  it('is an .mdx page that embeds the real example file', () => {
    expect(existsSync(docs('architecture.mdx'))).toBe(true);
    const page = readPage('architecture.mdx');
    expect(page).toMatch(
      /import\s+\w+\s+from\s+'\.\.\/\.\.\/\.\.\/\.\.\/examples\/passthrough-openrouter\.yaml\?raw'/,
    );
    expect(page).toMatch(/<Code\s+code=\{/);
  });

  it('does not keep an inline duplicate of the manifest', () => {
    const page = readPage('architecture.mdx');
    // No fenced yaml block declaring the kind should remain on the page.
    expect(page).not.toMatch(/```yaml[\s\S]*kind:\s*PassthroughModel/);
  });
});

describe('new example manifests', () => {
  it('minimal.yaml uses the operator namespace the webhook requires', () => {
    const m = readExample('models/minimal.yaml');
    expect(m).toMatch(/namespace:\s*nebari-llm-serving-system/);
    expect(m).toMatch(/kind:\s*LLMModel/);
    expect(m).not.toMatch(/namespace:\s*llm-serving\b/);
  });

  it('advanced-scheduling.yaml demonstrates all spec.advanced escape hatches', () => {
    const m = readExample('models/advanced-scheduling.yaml');
    for (const field of ['nodeSelector', 'tolerations', 'affinity', 'extraArgs', 'extraEnv']) {
      expect(m).toContain(field);
    }
  });
});
