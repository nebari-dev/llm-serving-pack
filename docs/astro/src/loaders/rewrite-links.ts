/**
 * Prefix root-absolute markdown links and images (`/...`) with `base`.
 * No-op when base is '/'. Leaves external (http/https), protocol-relative,
 * and anchor-only links untouched. Idempotent for already-prefixed paths.
 */
export function rewriteLinks(markdown: string, base: string): string {
  if (!base || base === '/') return markdown;
  const prefix = base.replace(/\/$/, '');
  // Matches the (target) part of []() where target starts with a single '/'.
  return markdown.replace(
    /(\]\()(\/[^)\s]*)(\))/g,
    (_m, open: string, target: string, close: string) => {
      if (target.startsWith('//')) return `${open}${target}${close}`;
      if (target.startsWith(`${prefix}/`)) return `${open}${target}${close}`;
      return `${open}${prefix}${target}${close}`;
    },
  );
}
