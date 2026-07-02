// docs/scripts/screenshots.mjs
import { chromium } from 'playwright';
import { mkdir } from 'node:fs/promises';

const BASE = process.env.PREVIEW_URL || 'http://localhost:4321';
const OUT = 'bakeoff-screenshots';
const shots = [
  { name: 'home', path: '/', width: 1440, height: 900 },
  { name: 'architecture', path: '/architecture/', width: 1440, height: 900 },
  { name: 'quickstart-dark', path: '/quickstart/', width: 1440, height: 900, dark: true },
  { name: 'mobile', path: '/', width: 390, height: 844 },
  { name: 'home-dark', path: '/', width: 1440, height: 900, dark: true },
];

await mkdir(OUT, { recursive: true });
const browser = await chromium.launch();
for (const s of shots) {
  const ctx = await browser.newContext({
    viewport: { width: s.width, height: s.height },
    colorScheme: s.dark ? 'dark' : 'light',
  });
  const page = await ctx.newPage();
  await page.goto(`${BASE}${s.path}`, { waitUntil: 'networkidle' });
  await page.screenshot({ path: `${OUT}/${s.name}.png`, fullPage: true });
  await ctx.close();
  console.log(`captured ${s.name}`);
}
await browser.close();
