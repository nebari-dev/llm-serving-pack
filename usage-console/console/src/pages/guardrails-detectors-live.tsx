import { Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import type { DetectorView } from '@/data/catalog'
import { int } from '@/lib/format'
import { useLive } from '@/state/live'
import { modeChip } from './guardrails-model'

// §7.5.7 detectors in api mode: the engine's own entity detectors, the live
// rules that name them, and what receipts recorded in the last 24 hours. The
// mockup's thresholds, custom-entity registry and false-positive queue have
// no backend, so they say so instead.

export function LiveDetectorsTab() {
  const { data, loaded } = useLive<DetectorView[]>('/detectors', [], 60_000)
  return (
    <div>
      <Section
        title="Detectors"
        description="What “Prompt contains entity” looks for. Each is a pattern match on the prompt text: there’s no confidence score, so there’s no threshold to tune."
      >
        {!loaded ? (
          <p className="text-sm text-muted-foreground">Loading detectors…</p>
        ) : (
          <div className="overflow-x-auto">
            <table aria-label="Detectors" className="w-full min-w-[52rem] text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs text-muted-foreground">
                  <th className="py-1.5 pr-3 font-medium">Entity</th>
                  <th className="py-1.5 pr-3 font-medium">How it matches</th>
                  <th className="py-1.5 pr-3 font-medium">Redacted as</th>
                  <th className="py-1.5 pr-3 font-medium">Used by live rules</th>
                  <th className="py-1.5 pr-3 text-right font-medium">Redacted, 24h</th>
                  <th className="py-1.5 text-right font-medium">Blocked, 24h</th>
                </tr>
              </thead>
              <tbody>
                {data.map((d) => (
                  <tr key={d.entity} className="border-b border-border align-top">
                    <td className="py-2 pr-3 font-medium">{d.entity}</td>
                    <td className="py-2 pr-3">
                      <span className="text-xs text-muted-foreground-strong">{d.kind}</span>
                      <code className="mt-0.5 block max-w-72 truncate font-mono text-xs text-muted-foreground" title={d.pattern}>
                        {d.pattern}
                      </code>
                    </td>
                    <td className="py-2 pr-3 font-mono text-xs">{d.placeholder}</td>
                    <td className="py-2 pr-3">
                      {d.usedBy.length === 0 ? (
                        <span className="text-xs text-muted-foreground">None</span>
                      ) : (
                        <span className="flex flex-col gap-1">
                          {d.usedBy.map((u) => (
                            <span key={u.rule} className="flex flex-wrap items-center gap-1.5 text-xs">
                              <span className="font-mono">
                                {u.rule} v{u.version}
                              </span>
                              <StateChip tone="neutral" className={modeChip[u.mode].className}>
                                {modeChip[u.mode].label}
                              </StateChip>
                              <span className="text-muted-foreground">{u.mode === 'monitor' ? `would ${u.action}` : u.action}</span>
                            </span>
                          ))}
                        </span>
                      )}
                    </td>
                    <td className="num py-2 pr-3 text-right font-mono text-xs">
                      {int(d.redactedRequests24h)}
                      {d.redactedMatches24h > d.redactedRequests24h && <span className="block text-muted-foreground">{int(d.redactedMatches24h)} matches</span>}
                    </td>
                    <td className="num py-2 text-right font-mono text-xs">{int(d.blocked24h)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          Counts are requests in the last 24 hours, from receipts: redactions by entity, and blocks that name the entity they matched. They include rules that were
          enforcing at the time, even if they’ve changed since. Monitor-mode matches aren’t counted: the receipt records “would redact” without the entity.
        </p>
      </Section>

      <Section title="Custom entities" description="Your own entity types, such as internal account IDs.">
        <p className="text-sm text-muted-foreground">
          Custom entities aren’t connected yet: the detectors above are built into the engine, and there’s no registry to add one.
        </p>
      </Section>

      <Section title="False-positive review" description="Reports that a redaction or block was wrong, from the receipt view.">
        <p className="text-sm text-muted-foreground">
          False-positive review isn’t connected yet: receipts have no way to report a match as wrong, so there’s nothing to review or count.
        </p>
      </Section>
    </div>
  )
}
