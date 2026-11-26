import { describe, it, expect } from 'vitest';
import { remark } from 'remark';
import remarkDirective from 'remark-directive';
import { visit } from 'unist-util-visit';
import { compile } from '@mdx-js/mdx';
import remarkPins, { parsePins } from '../src/plugins/remark-pins';

const pins = { AI_GATEWAY_VERSION: 'v1.1.0', CERT_MANAGER_VERSION: 'v1.17.2' };

describe('parsePins', () => {
  // Same cases as TestParsePins in operator/cmd/render-pins/pins_test.go.
  const cases: Array<{ name: string; in: string; want?: Record<string, string>; wantErr?: string }> = [
    { name: 'comments and blanks', in: '# c\n\nA=b\n  \n# d\nC=d=e\n', want: { A: 'b', C: 'd=e' } },
    { name: 'empty', in: '', want: {} },
    { name: 'quoted', in: 'A="b"\n', wantErr: 'line 1' },
    { name: 'single quoted', in: "# x\nA='b'\n", wantErr: 'line 2' },
    { name: 'space before =', in: 'A =b\n', wantErr: 'line 1' },
    { name: 'space after =', in: 'A= b\n', wantErr: 'line 1' },
    { name: 'dollar', in: 'A=b\nB=$VAR\n', wantErr: 'line 2' },
    { name: 'duplicate', in: 'A=b\nC=d\nA=e\n', wantErr: 'line 3' },
    { name: 'no equals', in: 'A\n', wantErr: 'line 1' },
    { name: 'empty key', in: '=b\n', wantErr: 'line 1' },
    { name: 'CRLF line endings', in: 'A=b\r\nC=d\r\n', want: { A: 'b', C: 'd' } },
  ];
  for (const c of cases) {
    it(c.name, () => {
      if (c.wantErr) {
        expect(() => parsePins(c.in)).toThrow(c.wantErr);
      } else {
        expect(parsePins(c.in)).toEqual(c.want);
      }
    });
  }
});

describe('remarkPins plugin', () => {
  it('replaces tokens in text, inlineCode, code and link.url', async () => {
    const md = [
      'Install %%pin:AI_GATEWAY_VERSION%% now.',
      '',
      'Run `helm --version %%pin:CERT_MANAGER_VERSION%%`.',
      '',
      '```bash',
      'kubectl apply -f https://x.io/%%pin:AI_GATEWAY_VERSION%%/a.yaml',
      '```',
      '',
      '[notes](https://x.io/releases/%%pin:CERT_MANAGER_VERSION%%)',
      '',
    ].join('\n');
    const out = String(await remark().use(remarkPins, { pins }).process(md));
    expect(out).toContain('Install v1.1.0 now.');
    expect(out).toContain('`helm --version v1.17.2`');
    expect(out).toContain('https://x.io/v1.1.0/a.yaml');
    expect(out).toContain('(https://x.io/releases/v1.17.2)');
    expect(out).not.toContain('%%pin:');
  });

  it('substitutes a token split across adjacent text nodes', async () => {
    // Defensive: any parser step that leaves a token split across adjacent text nodes.
    const tree: any = {
      type: 'root',
      children: [
        {
          type: 'paragraph',
          children: [
            { type: 'text', value: 'Install %%pin' },
            { type: 'text', value: ':AI_GATEWAY_VERSION%% now.' },
          ],
        },
      ],
    };
    (remarkPins({ pins }) as (t: any) => void)(tree);
    expect(tree.children[0].children).toEqual([{ type: 'text', value: 'Install v1.1.0 now.' }]);
  });

  it('substitutes a token that the directive syntax split into a textDirective', async () => {
    const tree: any = {
      type: 'root',
      children: [
        {
          type: 'paragraph',
          children: [
            { type: 'text', value: 'Install %%pin' },
            { type: 'textDirective', name: 'AI_GATEWAY_VERSION', attributes: {}, children: [] },
            { type: 'text', value: '%% now.' },
          ],
        },
      ],
    };
    (remarkPins({ pins }) as (t: any) => void)(tree);
    expect(tree.children[0].children).toEqual([{ type: 'text', value: 'Install v1.1.0 now.' }]);
  });

  it('compiles .mdx with tokens in prose, a code fence and a link', async () => {
    const mdx = [
      'Version %%pin:AI_GATEWAY_VERSION%% here.',
      '',
      '```bash',
      'helm install --version %%pin:CERT_MANAGER_VERSION%%',
      '```',
      '',
      '[rel](https://x.io/%%pin:AI_GATEWAY_VERSION%%)',
      '',
    ].join('\n');
    const out = String(await compile(mdx, { remarkPlugins: [[remarkPins, { pins }]] }));
    expect(out).toContain('v1.1.0');
    expect(out).toContain('helm install --version v1.17.2');
    expect(out).toContain('https://x.io/v1.1.0');
    expect(out).not.toContain('%%pin:');
  });

  it('throws on an unknown key', async () => {
    await expect(
      remark().use(remarkPins, { pins }).process('Use %%pin:NOPE_VERSION%%.'),
    ).rejects.toThrow('NOPE_VERSION');
  });

  const leftovers: Array<{ name: string; md: string }> = [
    { name: 'image url', md: '![alt](https://x.io/%%pin:AI_GATEWAY_VERSION%%.png)' },
    { name: 'html', md: '<div>%%pin:AI_GATEWAY_VERSION%%</div>' },
    { name: 'image alt', md: '![v %%pin:AI_GATEWAY_VERSION%%](/a.png)' },
    { name: 'link title', md: '[a](/b "v %%pin:AI_GATEWAY_VERSION%%")' },
  ];
  for (const c of leftovers) {
    it(`throws on a token left in another node type: ${c.name}`, async () => {
      await expect(remark().use(remarkPins, { pins }).process(c.md)).rejects.toThrow('%%pin:');
    });
  }
});

describe('directive fold leaves other directives alone', () => {
  const cases: Array<{ name: string; md: string; want: number }> = [
    { name: 'bare directive after ordinary text', md: 'see :foo here', want: 1 },
    { name: 'directive with a label', md: 'see :foo[label] here', want: 1 },
    { name: 'directive with attributes', md: 'see :foo{a=b} here', want: 1 },
    { name: 'container directive aside', md: ':::note\nbody\n:::\n', want: 1 },
    { name: 'directive after text not ending in %%pin', md: 'cost %%:foo here', want: 1 },
  ];
  const directives = (tree: any) => {
    const out: any[] = [];
    visit(tree, (n: any) => {
      if (/Directive$/.test(n.type)) out.push(JSON.parse(JSON.stringify(n, (k, v) => (k === 'position' ? undefined : v))));
    });
    return out;
  };
  for (const c of cases) {
    it(c.name, async () => {
      // Same parser setup as the build: remark-directive is on, then remark-pins.
      const proc = remark().use(remarkDirective).use(remarkPins, { pins });
      const before = directives(proc.parse(c.md));
      expect(before).toHaveLength(c.want);
      const after = directives(await proc.run(proc.parse(c.md)));
      expect(after).toEqual(before);
    });
  }
});
