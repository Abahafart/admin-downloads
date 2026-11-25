<script lang="ts">
  // Thin progress bar. The fill color comes from `tone` (a token), but the
  // state is always also given as text next to it (StateLabel) and in the
  // accessible name.
  import type { Tone } from '../lib/store/status';
  let {
    value,
    tone = 'accent',
    label = '',
    height = 4,
    full = false,
  }: { value: number; tone?: Tone; label?: string; height?: number; full?: boolean } = $props();
  const pct = $derived(full ? 100 : Math.max(0, Math.min(100, value)));
</script>

<div
  class="bar"
  role="progressbar"
  aria-valuemin="0"
  aria-valuemax="100"
  aria-valuenow={Math.round(pct)}
  aria-label={label || undefined}
  style:--h="{height}px"
>
  <div class="fill tone-{tone}" style:width="{pct}%"></div>
</div>

<style>
  .bar { flex: 1; min-width: 40px; height: var(--h); border-radius: calc(var(--h) / 2); background: var(--track); overflow: hidden; }
  .fill { height: 100%; border-radius: inherit; background: var(--accent); transition: width 0.4s linear; }
  .fill.tone-warn { background: var(--warn); }
  .fill.tone-err { background: var(--err); }
  .fill.tone-paused { background: var(--paused); }
  .fill.tone-done { background: var(--done); }
  .fill.tone-muted { background: var(--paused); }
  .fill.tone-text { background: var(--text); }
</style>
