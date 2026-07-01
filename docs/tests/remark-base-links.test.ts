import { describe, it, expect } from 'vitest';
import { remark } from 'remark';
import remarkBaseLinks, { prefixUrl } from '../src/plugins/remark-base-links';

describe('prefixUrl', () => {
  const cases: Array<{ name: string; url: string; base: string; want: string }> = [
    { name: 'base "/" leaves links unchanged', url: '/quickstart/', base: '/', want: '/quickstart/' },
    { name: 'sub-path base prefixes internal links', url: '/quickstart/', base: '/llm-serving-pack/', want: '/llm-serving-pack/quickstart/' },
    { name: 'prefixes image paths', url: '/install-production-screenshots/a.png', base: '/llm-serving-pack/', want: '/llm-serving-pack/install-production-screenshots/a.png' },
    { name: 'never rewrites external links', url: 'https://example.com', base: '/llm-serving-pack/', want: 'https://example.com' },
    { name: 'never rewrites protocol-relative links', url: '//example.com/x', base: '/llm-serving-pack/', want: '//example.com/x' },
    { name: 'never rewrites anchor-only links', url: '#section', base: '/llm-serving-pack/', want: '#section' },
    { name: 'preserves anchors on internal links', url: '/installation/#step-1', base: '/llm-serving-pack/', want: '/llm-serving-pack/installation/#step-1' },
    { name: 'idempotent on already-prefixed links', url: '/llm-serving-pack/quickstart/', base: '/llm-serving-pack/', want: '/llm-serving-pack/quickstart/' },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(prefixUrl(c.url, c.base)).toBe(c.want);
    });
  }
});

describe('remarkBaseLinks plugin', () => {
  it('rewrites link and image urls in a markdown document', async () => {
    const md = 'See [Quickstart](/quickstart/) and ![img](/img/a.png) and [ext](https://x.io)';
    const out = String(
      await remark().use(remarkBaseLinks, { base: '/llm-serving-pack/' }).process(md),
    );
    expect(out).toContain('(/llm-serving-pack/quickstart/)');
    expect(out).toContain('(/llm-serving-pack/img/a.png)');
    expect(out).toContain('(https://x.io)');
  });

  it('is a no-op when base is "/"', async () => {
    const md = '[Q](/quickstart/)';
    const out = String(await remark().use(remarkBaseLinks, { base: '/' }).process(md));
    expect(out).toContain('(/quickstart/)');
  });
});
