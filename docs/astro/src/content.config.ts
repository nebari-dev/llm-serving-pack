import { defineCollection } from 'astro:content';
import { docsSchema } from '@astrojs/starlight/schema';
import { hugoContentLoader } from './loaders/hugo-content';

export const collections = {
  docs: defineCollection({
    loader: hugoContentLoader({ dir: '../site/content' }),
    schema: docsSchema(),
  }),
};
