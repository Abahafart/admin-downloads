<script lang="ts">
  // The textual state of a download/package: "Descargando · 1:24",
  // "Cuota por IP · 04:12", "Fallida · cuota del archivo"... Color is only a
  // complement (class tone-*), never the only signal.
  import type { StatusInfo } from '../lib/store/status';
  let { status, short = false, colored = true }: { status: StatusInfo; short?: boolean; colored?: boolean } = $props();
</script>

<span class="state {colored ? `tone-${status.tone}` : ''}" data-state={status.state}>{short ? status.short : status.label}</span>

<style>
  .state { white-space: nowrap; }
  .tone-text { color: inherit; }
</style>
