// docs/astro.config.mjs
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import react from '@astrojs/react';
import tailwindcss from '@tailwindcss/vite';
import rehypeMermaid from 'rehype-mermaid';
import { fileURLToPath } from 'url';
import path from 'path';
import remarkBaseLinks from './src/plugins/remark-base-links';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  // Base defaults to '/' for the local bake-off. Override via BASE env if deployed.
  base: process.env.BASE || '/',
  site: process.env.SITE,
  integrations: [
    react(),
    starlight({
      title: 'LLM Serving Pack',
      description:
        'Kubernetes operator for serving LLMs on Nebari via llm-d, with per-model OIDC access control, API key management, and Envoy AI Gateway integration.',
      logo: {
        light: './src/assets/logo.svg',
        dark: './src/assets/logo-dark.svg',
        replacesTitle: false,
      },
      customCss: [
        '@fontsource-variable/geist',
        '@fontsource/ibm-plex-mono',
        './src/styles/nebari-tokens.css',
        './src/styles/starlight-theme.css',
      ],
      components: {
        SocialIcons: './src/components/SocialIcons.astro',
      },
      sidebar: [
        {
          label: 'Documentation',
          items: [
            { label: 'Quickstart', slug: 'quickstart' },
            { label: 'Installation', slug: 'installation' },
            { label: 'Local Development', slug: 'local-development' },
            { label: 'Shared Storage', slug: 'shared-storage' },
            { label: 'Troubleshooting', slug: 'troubleshooting' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Configuration', slug: 'configuration' },
            { label: 'Architecture', slug: 'architecture' },
            { label: 'CI/CD and Releasing', slug: 'cicd-and-releasing' },
          ],
        },
      ],
    }),
  ],
  markdown: {
    // syntaxHighlight false on mermaid so the plugin sees raw graph source.
    syntaxHighlight: { type: 'shiki', excludeLangs: ['mermaid'] },
    remarkPlugins: [[remarkBaseLinks, { base: process.env.BASE || '/' }]],
    rehypePlugins: [[rehypeMermaid, { strategy: 'inline-svg' }]],
  },
  vite: {
    plugins: [tailwindcss()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
  },
});
