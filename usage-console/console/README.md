# Gateway console: front-end mockup

A clickable front-end mockup of the **Nebari Gateway Console** described in [`../docs/spec.md`](../docs/spec.md), built on the [nebari-design](https://github.com/nebari-dev/nebari-design) system.

There is no backend. Everything runs on seeded synthetic data (the spec's "demo tenant", §7.5.1), and a simulated live receipt stream feeds Traffic.

```bash
cd console
npm install
npm run dev        # http://localhost:5173
npm run build      # type-check + production build
```

## Stack

React 19, TypeScript, Vite, Tailwind v4, and react-router. This is the stack spec §4.1 suggests.

## How nebari-design is used

nebari-design is a shadcn registry, not an npm package. Its sources were vendored as the shadcn CLI would install them, from `nebari-dev/nebari-design@d5fe6ee`:

| From the registry | Into this app |
|---|---|
| `registry/nebari/globals.css` (the `@nebari/theme` tokens) | `src/globals.css` |
| `registry/nebari/ui/*` | `src/components/ui/*` |
| `registry/nebari/hooks/*` | `src/hooks/*` |
| `registry/nebari/lib/*` | `src/lib/utils.ts`, `src/lib/date.ts` |
| Brand symbol and logos | `src/assets/brand/` |

These files are **upstream-managed**: they are customised only at the call site, never edited. To re-sync, run `npx shadcn add @nebari/<name>` once `components.json` points at the `@nebari` registry. `@base-ui/react` is pinned to `1.6.0` to match the registry's lockfile.

App-level additions live beside the vendored files, not inside them:

- **`src/app.css`** adds the tokens spec §8 asks nebari-design for:
  - the five-colour verdict ramp (allowed, redacted, rerouted, blocked, degraded), with a foreground, background, border and bar colour per verdict in both themes
  - the environment accent
  - the three-step density scale
  - the header recipe tokens
- **`src/components/gw/`** holds the spec §8 primitives:
  - `VerdictBadge`, `Money` / `TokenCount` / `Duration`
  - `ProvenanceBadge`, `SyncStateIndicator`
  - `DecisionTrace`, `DiffView`
  - `StackedArea` / `StackedBars` / `Sparkline` (each with a table fallback)
  - the receipt drawer
- **`src/components/shell/`** holds the app shell: the nebari header recipe, sidebar, degradation banner, and ⌘K command palette.

## What's in the mockup

| Spec | Screen |
|---|---|
| §7.5.2 | **Overview**: status strip, verdict-stacked traffic, three numbers, *what changed* joined to traffic effect, attention list |
| §7.5.3 | **Traffic**: live stream table with freeze-on-hover and an "N new" resume, in-flight rows that settle in place, a verdict edge bar, URL-serialized filters, sampling mode, configurable pinned columns, density modes |
| §7.5.4 | **Receipt drawer** (`?receipt=<id>`, deep-linkable): header, decision trace, policy, usage and price basis, gated content reveal, related |
| §7.5.5 | **Spend**: trend and breakdown, budgets stated in words, projection with its basis, savings analysis, export |
| §7.5.6 | **Routing** and **Models**: provenance-first backend list, CRD diff before apply, adopt, drift and reconcile-failed states, generated YAML and export |
| §7.5.7 | **Guardrails**: rules, rule builder and replay in three panes, monitor-mode publish, detectors, versions |
| §7.5.8 | **Keys**: required expiry, show-once secret with acknowledgement, blast-radius revoke, rotation |
| §7.5.9 | **Activity**: config changes and traffic on one timeline |
| §7.5.1 | **Onboarding**: connect a provider, key swap diff, then a waiting state that resolves into your first receipt |

Global elements: a production/staging accent, one shared time range, the ⌘K palette (paste a trace ID to jump to its receipt), a ranked degradation banner, and a light/dark/system theme via the nebari theme picker.

## Deliberate departures and notes

- **No all-caps labels.** Spec §7.3 says sentence case throughout. Nebari's `DropdownMenuGroupLabel` and `SidebarGroupLabel` default to uppercase, so they are overridden at the call site with `normal-case`.
- **Magenta stays brand-only.** Per the spec's palette rule (§7.2), saturated colour is used only for verdict and sync state. Charts that aren't about verdicts use nebari's `--chart-*` series.
- **Motion** follows nebari `AGENTS.md`: everything is `motion-safe:` with duration tokens. The traffic table's only motion is a one-time row-arrival highlight. Under `prefers-reduced-motion` it becomes a static left-edge marker.
- **The traffic table is not virtualised** in this mockup; it caps at 300 visible rows. The spec's `DataTable` / `StreamTable` would add virtualisation.
