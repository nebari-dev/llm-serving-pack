// docs/astro/astro.config.mjs
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import react from '@astrojs/react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  // Base defaults to '/' for the local bake-off. Override via BASE env if deployed.
  base: process.env.BASE || '/',
  integrations: [
    react(),
    starlight({
      title: 'LLM Serving Pack',
      description:
        'Kubernetes operator for serving LLMs on Nebari via llm-d, with per-model OIDC access control, API key management, and Envoy AI Gateway integration.',
      customCss: [
        '@fontsource-variable/geist',
        '@fontsource/ibm-plex-mono',
        './src/styles/nebari-tokens.css',
        './src/styles/starlight-theme.css',
      ],
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/nebari-dev/nebari-llm-serving-pack' },
      ],
    }),
  ],
  vite: { plugins: [tailwindcss()] },
});
