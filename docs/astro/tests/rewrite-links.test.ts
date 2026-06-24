import { describe, it, expect } from 'vitest';
import { rewriteLinks } from '../src/loaders/rewrite-links';

describe('rewriteLinks', () => {
  const cases: Array<{ name: string; md: string; base: string; want: string }> = [
    {
      name: 'base "/" leaves internal links unchanged',
      md: 'See [Quickstart](/quickstart/) and ![img](/install-production-screenshots/a.png)',
      base: '/',
      want: 'See [Quickstart](/quickstart/) and ![img](/install-production-screenshots/a.png)',
    },
    {
      name: 'sub-path base prefixes internal links and images',
      md: '[Q](/quickstart/) ![i](/install-production-screenshots/a.png)',
      base: '/nebari-llm-serving-pack/',
      want: '[Q](/nebari-llm-serving-pack/quickstart/) ![i](/nebari-llm-serving-pack/install-production-screenshots/a.png)',
    },
    {
      name: 'never rewrites external or anchor links',
      md: '[ext](https://example.com) [anchor](#section)',
      base: '/nebari-llm-serving-pack/',
      want: '[ext](https://example.com) [anchor](#section)',
    },
    {
      name: 'preserves anchors on internal links',
      md: '[s](/installation/#step-1)',
      base: '/nebari-llm-serving-pack/',
      want: '[s](/nebari-llm-serving-pack/installation/#step-1)',
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(rewriteLinks(c.md, c.base)).toBe(c.want);
    });
  }
});
