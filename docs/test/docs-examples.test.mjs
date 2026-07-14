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

describe('quickstart', () => {
  it('has a Route to an external provider section that embeds the example', () => {
    const page = readPage('quickstart.mdx');
    expect(page).toMatch(/##\s+Route to an external provider/);
    expect(page).toMatch(
      /import\s+\w+\s+from\s+'\.\.\/\.\.\/\.\.\/\.\.\/examples\/passthrough-openrouter\.yaml\?raw'/,
    );
    expect(page).toMatch(/kubectl create secret generic openrouter-api-key/);
    expect(page).toMatch(/llm\.[^\s]*\/v1\/chat\/completions/);
  });

  it('shows how to create the hf-token secret inline where authSecretName appears', () => {
    const page = readPage('quickstart.mdx');
    expect(page).toMatch(/kubectl create secret generic hf-token/);
    expect(page).toMatch(/HF_TOKEN/);
  });
});

describe('configuration', () => {
  it('documents every PassthroughModel field', () => {
    const page = readPage('configuration.mdx');
    expect(page).toMatch(/##\s+PassthroughModel CRD reference/);
    for (const f of [
      'provider.hostname', 'provider.port', 'provider.schemaVersion',
      'provider.credentialSecretName', 'models.catchAll', 'models.declared',
      'access', 'endpoints',
    ]) {
      expect(page).toContain(f);
    }
  });

  it('embeds minimal and advanced examples instead of inlining them', () => {
    const page = readPage('configuration.mdx');
    expect(page).toMatch(/examples\/models\/minimal\.yaml\?raw/);
    expect(page).toMatch(/examples\/models\/advanced-scheduling\.yaml\?raw/);
    // the broken hand-written minimal manifest is gone
    expect(page).not.toMatch(/namespace:\s*llm-serving\b/);
  });
});

describe('shared-storage', () => {
  it('embeds complete OCI and gated-HF manifests', () => {
    const page = readPage('shared-storage.mdx');
    expect(page).toMatch(/examples\/models\/oci-model\.yaml\?raw/);
    expect(page).toMatch(/examples\/models\/devstral-small\.yaml\?raw/);
  });

  it('devstral example header comment uses the operator namespace', () => {
    const m = readExample('models/devstral-small.yaml');
    expect(m).not.toMatch(/create secret generic hf-token -n llm-serving\b/);
  });
});

describe('troubleshooting', () => {
  it('has an external-provider (PassthroughModel) failure section', () => {
    const page = readPage('troubleshooting.md');
    expect(page).toMatch(/PassthroughModel|external provider/i);
    expect(page).toMatch(/ApplyFailed/);
    expect(page).toMatch(/credentialSecretName/);
    expect(page).toMatch(/catch-all|catchAll/i);
  });
});

describe('manifest reconciliation (NIC source of truth)', () => {
  it('argocd-application.yaml uses v0.1.1 + NIC clusterIssuer + longhorn', () => {
    const m = readExample('argocd-application.yaml');
    expect(m).toMatch(/targetRevision:\s*v0\.1\.1\b/);
    expect(m).toMatch(/clusterIssuer:\s*letsencrypt-issuer\b/);
    expect(m).toMatch(/storageClassName:\s*longhorn\b/);
    expect(m).not.toMatch(/v0\.1\.0-alpha\.7\b/);
    expect(m).not.toMatch(/letsencrypt-production\b/);
  });
  it('nvidia-gpu-operator.yaml uses the documented v25.10.1', () => {
    const m = readExample('nvidia-gpu-operator.yaml');
    expect(m).toMatch(/targetRevision:\s*v25\.10\.1\b/);
    expect(m).not.toMatch(/v26\.3\.0\b/);
  });
  it('envoy-ai-gateway.yaml drops the oci:// prefix (NIC form)', () => {
    const m = readExample('envoy-ai-gateway.yaml');
    expect(m).not.toMatch(/oci:\/\//);
    expect(m).toMatch(/repoURL:\s*docker\.io\/envoyproxy\b/);
  });
  it('installation.md recommends the v0.1.1 pack version', () => {
    const page = readPage('installation.md');
    expect(page).toMatch(/v0\.1\.1\b/);
    expect(page).not.toMatch(/targetRevision:\s*v0\.1\.0-alpha\.9\b/);
  });
});
