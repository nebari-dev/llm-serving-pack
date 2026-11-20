# Contracts

Contracts the operator owns or depends on. Changing one updates its entry in the same PR.

### Chart value `operator.webhook.clusterIssuer`
**Owner:** `charts/nebari-llm-serving` (`values.yaml`, `templates/operator-webhook-cert.yaml`) · **Consumers:** every chart install, through the webhook serving Certificate's `issuerRef`; the docs site (`configuration.mdx`, `installation.md`, `quickstart.mdx`, `upgrading.mdx`) and `README.md` (verified: `rg -n "operator\.webhook\.clusterIssuer|\.Values\.operator\.webhook" --glob '!node_modules' --glob '!.worktrees' .`). The kind dev cluster installs from `dev/manifests/`, not the chart, and is unaffected.
**Promises:** A string naming a cert-manager `ClusterIssuer` (the `kind` stays `ClusterIssuer`). It signs the operator's admission webhook serving Certificate, whose DNS names are `<svc>.<ns>.svc` and `<svc>.<ns>.svc.cluster.local`. Default `nebari-ca-issuer`, which NIC creates in releases that include nebari-infrastructure-core#787; before this value existed the issuer was hardcoded to `selfsigned-issuer`. A missing key or `operator.webhook` block (for example values reused from an older release with `helm upgrade --reuse-values`) renders the default. An explicitly empty value fails the render with `operator.webhook.clusterIssuer is required: ...`. The chart does not create the issuer.
**Since:** 2026-11-20
