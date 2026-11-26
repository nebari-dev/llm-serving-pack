import { describe, it, expect } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import yaml from 'js-yaml';

const docs = (name) =>
  fileURLToPath(new URL(`../src/content/docs/${name}`, import.meta.url));
const example = (name) =>
  fileURLToPath(new URL(`../../examples/${name}`, import.meta.url));

export const readPage = (name) => readFileSync(docs(name), 'utf8');
export const readExample = (name) => readFileSync(example(name), 'utf8');

describe('architecture: PassthroughModel embed', () => {
  it('uses themed code blocks with captions for both provider examples', () => {
    const page = readPage('architecture.mdx');
    expect(page).toContain("import Code from '../../components/ExampleCode.astro'");
    expect(page).not.toContain('astro:components');
    for (const [binding, filename] of [
      ['passthroughExample', 'passthrough-openrouter.yaml'],
      ['bedrockExample', 'passthrough-bedrock.yaml'],
    ]) {
      expect(page).toContain(`<Code code={${binding}} lang="yaml" title="examples/${filename}" />`);
    }
  });

  it('adapts imported example code to the selected site theme', () => {
    const component = readFileSync(new URL('../src/components/ExampleCode.astro', import.meta.url), 'utf8');
    expect(component).toContain("themes={{ light: 'github-light-default', dark: 'github-dark-default' }}");
    expect(component).toContain("[data-theme='dark']");
    expect(component).toContain('var(--shiki-dark)');
    expect(component).toContain('<figcaption>{title}</figcaption>');
  });

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

describe('ExampleCode adoption', () => {
  it('every page embedding examples uses ExampleCode, not astro:components', () => {
    for (const name of [
      'architecture.mdx',
      'bedrock.mdx',
      'configuration.mdx',
      'quickstart.mdx',
      'self-hosted.mdx',
      'shared-storage.mdx',
    ]) {
      const page = readPage(name);
      expect(page).toContain("import Code from '../../components/ExampleCode.astro'");
      expect(page).not.toContain('astro:components');
    }
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
      'provider.backend', 'provider.backend.bedrock.region',
      'provider.credential', 'provider.tls.caConfigMapName', 'access', 'endpoints',
    ]) {
      expect(page).toContain(f);
    }
  });

  it('documents the Bedrock workload-identity example', () => {
    const page = readPage('configuration.mdx');
    const example = readExample('passthrough-bedrock.yaml');
    expect(page).toContain('examples/passthrough-bedrock.yaml?raw');
    expect(example).toContain('type: Bedrock');
    expect(example).toContain('type: WorkloadIdentity');
  });

  it('embeds minimal and advanced examples instead of inlining them', () => {
    const page = readPage('configuration.mdx');
    expect(page).toMatch(/examples\/models\/minimal\.yaml\?raw/);
    expect(page).toMatch(/examples\/models\/advanced-scheduling\.yaml\?raw/);
    // the broken hand-written minimal manifest is gone
    expect(page).not.toMatch(/namespace:\s*llm-serving\b/);
  });
});

describe('self-hosted', () => {
  it('embeds the llama.cpp example, which trusts a CA ConfigMap', () => {
    const page = readPage('self-hosted.mdx');
    const example = readExample('passthrough-llamacpp.yaml');
    expect(page).toContain('examples/passthrough-llamacpp.yaml?raw');
    expect(page).not.toMatch(/```yaml[\s\S]*kind:\s*PassthroughModel/);
    expect(example).toMatch(/namespace:\s*nebari-llm-serving-system/);
    expect(example).toMatch(/caConfigMapName:\s*\S+/);
  });

  it('keeps the llama-server alias and the declared model id in step', () => {
    const page = readPage('self-hosted.mdx');
    const example = readExample('passthrough-llamacpp.yaml');
    const alias = page.match(/--alias\s+(\S+)/)[1];
    expect(example).toMatch(new RegExp(`declared:[\\s\\S]*-\\s+${alias}\\b`));
    for (const flag of ['--ssl-cert-file', '--ssl-key-file', '--api-key-file']) {
      expect(page).toContain(flag);
    }
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
  it('argocd-application.yaml uses the published OCI chart (0.1.4) + NIC clusterIssuer + longhorn', () => {
    const m = readExample('argocd-application.yaml');
    expect(m).toMatch(/repoURL:\s*quay\.io\/nebari\/charts\b/);
    expect(m).toMatch(/chart:\s*nebari-llm-serving\b/);
    expect(m).toMatch(/targetRevision:\s*"0\.1\.4"/);
    expect(m).toMatch(/clusterIssuer:\s*letsencrypt-issuer\b/);
    expect(m).toMatch(/storageClassName:\s*longhorn\b/);
    expect(m).not.toMatch(/path:\s*charts\/nebari-llm-serving\b/);
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
  it('installation.md installs the pack from the published OCI chart (0.1.2)', () => {
    const page = readPage('installation.md');
    expect(page).toMatch(/repoURL:\s*quay\.io\/nebari\/charts\b/);
    expect(page).toMatch(/chart:\s*nebari-llm-serving\b/);
    expect(page).toMatch(/targetRevision:\s*"0\.1\.2"/);
    expect(page).not.toMatch(/path:\s*charts\/nebari-llm-serving\b/);
    expect(page).not.toMatch(/targetRevision:\s*v0\.1\.0-alpha\.9\b/);
  });
});

const CERT_MANAGER_VALUES = {
  controller: { mutatingWebhook: { certManager: { enable: true } } },
};

const loadAllYaml = (text) => yaml.loadAll(text).filter(Boolean);

const fencedYaml = (md) =>
  [...md.matchAll(/```yaml\n([\s\S]*?)```/g)].map((m) => m[1]);

const findApp = (parsed, name) =>
  parsed.find((d) => d?.kind === 'Application' && d?.metadata?.name === name);

const exampleApps = () => loadAllYaml(readExample('envoy-ai-gateway.yaml'));

const installApps = () =>
  fencedYaml(readPage('installation.md')).flatMap((block) => {
    try {
      return loadAllYaml(block);
    } catch {
      return [];
    }
  });

describe('AI Gateway webhook TLS is cert-manager backed', () => {
  it('the example enables cert-manager mode on the controller Application', () => {
    const ctrl = findApp(exampleApps(), 'envoy-ai-gateway');
    expect(ctrl).toBeDefined();
    expect(yaml.load(ctrl.spec.source.helm.values)).toEqual(CERT_MANAGER_VALUES);
  });

  it('does not put the values on the CRDs Application', () => {
    const crds = findApp(exampleApps(), 'envoy-ai-gateway-crds');
    expect(crds).toBeDefined();
    expect(crds.spec.source.helm?.values).toBeUndefined();
  });

  it('the installation guide manifest carries byte-identical values', () => {
    const inline = findApp(installApps(), 'envoy-ai-gateway');
    expect(inline).toBeDefined();
    const fromDocs = yaml.load(inline.spec.source.helm.values);
    const fromExample = yaml.load(
      findApp(exampleApps(), 'envoy-ai-gateway').spec.source.helm.values,
    );
    expect(fromDocs).toEqual(fromExample);
    expect(fromDocs).toEqual(CERT_MANAGER_VALUES);
  });

  it('both pin the same chart version, and that version is v1.1.0', () => {
    // Deliberately strict. Bumping the AI Gateway chart is issue #175, and it
    // must be done together with scripts/check-render-determinism.sh and a
    // fresh render-determinism run, because a newer chart may reintroduce
    // render-time certificate generation. Agreement alone is not enough: a
    // lockstep bump of both files to the same new version would otherwise
    // pass unnoticed.
    const a = findApp(exampleApps(), 'envoy-ai-gateway').spec.source.targetRevision;
    const b = findApp(installApps(), 'envoy-ai-gateway').spec.source.targetRevision;
    expect(a).toBe(b);
    expect(a).toBe('v1.1.0');
  });
});

describe('render-determinism script stays in sync with the example', () => {
  const script = () =>
    readFileSync(
      fileURLToPath(new URL('../../scripts/check-render-determinism.sh', import.meta.url)),
      'utf8',
    );

  it('pins the same chart version as the example', () => {
    const inScript = script().match(/^CHART_VERSION="([^"]+)"/m)?.[1];
    const inExample = findApp(exampleApps(), 'envoy-ai-gateway').spec.source.targetRevision;
    expect(inScript).toBe(inExample);
  });

  it('uses the same values as the example', () => {
    const block = script().match(/^VALUES='([\s\S]*?)'$/m)?.[1];
    expect(block).toBeDefined();
    expect(yaml.load(block)).toEqual(CERT_MANAGER_VALUES);
  });
});

describe('AI Gateway webhook troubleshooting states the correct root cause', () => {
  const page = () => readPage('troubleshooting.md');

  it('does not blame startup CA generation or pod rescheduling', () => {
    const text = page();
    expect(text).not.toMatch(/controller\s+generates/i);
    expect(text).not.toMatch(/generates?\s+a\s+new\s+CA/i);
    expect(text).not.toMatch(/new\s+pod\s+generates/i);
  });

  it('names render-time generation as the cause', () => {
    expect(page()).toMatch(/render[-\s]time/i);
  });

  it('points at cert-manager mode as the remedy', () => {
    expect(page()).toMatch(/certManager|cert-manager mode/);
  });

  it('never labels rollout restart as the fix', () => {
    expect(page()).not.toMatch(
      /\*\*Fix:[\s\S]{0,300}?rollout restart deploy -n envoy-ai-gateway-system/,
    );
  });

  it('documents the dry-run admission probe', () => {
    const text = page();
    expect(text).toMatch(/--dry-run=server/);
    expect(text).toMatch(/app\.kubernetes\.io\/managed-by:\s*envoy-gateway/);
  });

  it('every probe carries the gateway owner labels the mutator needs to inject', () => {
    // AI Gateway v1.1.0 injects ai-gateway-extproc only into pods labelled with
    // the owning Gateway, as real Envoy proxy pods are. Without these two
    // labels the probe is admitted but never shows the sidecar.
    const probes = page().split('generateName: aigw-webhook-probe-').slice(1)
      .map((rest) => rest.split('spec:')[0]);
    expect(probes.length).toBeGreaterThan(0);
    for (const labels of probes) {
      expect(labels).toMatch(/gateway\.envoyproxy\.io\/owning-gateway-name:\s*nebari-gateway/);
      expect(labels).toMatch(/gateway\.envoyproxy\.io\/owning-gateway-namespace:\s*envoy-gateway-system/);
    }
  });

  it('carries the measured rotation window', () => {
    expect(page()).toMatch(/43 and 56 seconds/);
  });
});

describe('AI Gateway webhook installation runbook states the correct root cause', () => {
  const page = () => readPage('installation.md');

  it('does not blame startup CA generation or pod rescheduling', () => {
    const text = page();
    expect(text).not.toMatch(/controller\s+generates/i);
    expect(text).not.toMatch(/generates?\s+a\s+new\s+CA/i);
    expect(text).not.toMatch(/new\s+pod\s+generates/i);
  });

  it('names render-time generation as the cause', () => {
    expect(page()).toMatch(/render[-\s]time/i);
  });

  it('points at cert-manager mode as the remedy', () => {
    expect(page()).toMatch(/cert-manager mode/);
  });

  it('never labels rollout restart as the fix', () => {
    expect(page()).not.toMatch(
      /\*\*Fix:[\s\S]{0,300}?rollout restart deploy -n envoy-ai-gateway-system/,
    );
  });

  it('does not publish the unsafe rollout-restart workaround', () => {
    // Section 12.2 defers to troubleshooting.md for emergency repair, so this
    // command form must not appear on this page at all. The `deploy/NAME -n NS`
    // form used by the unrelated PostRouteModify crashloop fix is a different
    // remedy for a different bug and is deliberately not matched here.
    expect(page()).not.toMatch(/rollout restart deploy -n envoy-ai-gateway-system/);
  });

  it('documents the dry-run admission probe', () => {
    const text = page();
    expect(text).toMatch(/--dry-run=server/);
    expect(text).toMatch(/app\.kubernetes\.io\/managed-by:\s*envoy-gateway/);
  });

  it('every probe carries the gateway owner labels the mutator needs to inject', () => {
    // AI Gateway v1.1.0 injects ai-gateway-extproc only into pods labelled with
    // the owning Gateway, as real Envoy proxy pods are. Without these two
    // labels the probe is admitted but never shows the sidecar.
    const probes = page().split('generateName: aigw-webhook-probe-').slice(1)
      .map((rest) => rest.split('spec:')[0]);
    expect(probes.length).toBeGreaterThan(0);
    for (const labels of probes) {
      expect(labels).toMatch(/gateway\.envoyproxy\.io\/owning-gateway-name:\s*nebari-gateway/);
      expect(labels).toMatch(/gateway\.envoyproxy\.io\/owning-gateway-namespace:\s*envoy-gateway-system/);
    }
  });

  it('documents the cainjector prerequisite', () => {
    expect(page()).toMatch(/cainjector/);
  });
});
