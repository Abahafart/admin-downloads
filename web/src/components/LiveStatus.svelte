<script lang="ts">
  // The fixed-place status every style shows (PLAN.md 7.1): total speed,
  // current exit, public IP and the clipboard monitor (toggle). `layout`
  // only changes the arrangement; styles may also build their own from the
  // same `app` fields.
  import { app } from '../lib/store/app.svelte';
  import { formatSpeed } from '../lib/store/format';
  let { layout = 'rows', toggle = true }: { layout?: 'rows' | 'inline'; toggle?: boolean } = $props();
  const speed = $derived(app.totalSpeed > 0 ? formatSpeed(app.totalSpeed) : '0 MB/s');
</script>

<div class="live {layout}" aria-label="Estado de la red">
  <div class="item"><span class="k">Total</span><span class="v tabular" aria-live="off">{speed}</span></div>
  <div class="item"><span class="k">Salida</span><span class="v">{app.currentEgressLabel}</span></div>
  <div class="item"><span class="k">IP</span><span class="v tabular">{app.publicIP || '—'}</span></div>
  {#if !app.clipboard.available}
    <div class="item" title={app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio'}>
      <span class="k">Portapapeles</span><span class="v muted">{app.clipboard.error ? 'No disponible' : 'Solo en escritorio'}</span>
    </div>
  {:else if toggle}
    <label class="item clip">
      <span class="k">Portapapeles</span>
      <input
        type="checkbox"
        checked={app.clipboardEnabled}
        onchange={(e) => app.setClipboard((e.currentTarget as HTMLInputElement).checked)}
      />
      <span class="sr-only">{app.clipboardEnabled ? 'vigilando' : 'en pausa'}</span>
    </label>
  {:else}
    <div class="item"><span class="k">Portapapeles</span><span class="v">{app.clipboardEnabled ? 'Vigilando' : 'En pausa'}</span></div>
  {/if}
  {#if !app.live && app.status === 'ready'}
    <div class="item"><span class="k">Conexión</span><span class="v tone-warn">Reconectando…</span></div>
  {/if}
</div>

<style>
  .live { display: flex; color: var(--muted); font-size: 12px; }
  .rows { flex-direction: column; gap: 10px; }
  .inline { flex-wrap: wrap; gap: 6px 18px; align-items: center; }
  .item { display: flex; justify-content: space-between; gap: 8px; }
  .inline .item { justify-content: flex-start; }
  .v { color: var(--text); }
  .clip { align-items: center; min-height: 28px; cursor: pointer; }
  .clip input { width: 16px; height: 16px; margin: 0; accent-color: var(--accent); cursor: pointer; }
</style>
