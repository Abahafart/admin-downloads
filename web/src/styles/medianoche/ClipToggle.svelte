<script lang="ts">
  // "Portapapeles vigilado" as a real toggle button (desktop app / mock);
  // in a browser it only says where the monitor exists.
  import { app } from '../../lib/store/app.svelte';
</script>

{#if app.clipboard.available}
  <button type="button" class="clip" aria-pressed={app.clipboardEnabled} onclick={() => app.setClipboard(!app.clipboardEnabled)}>
    <span class="dot" class:on={app.clipboardEnabled} aria-hidden="true"></span>
    {app.clipboardEnabled ? 'Portapapeles vigilado' : 'Portapapeles en pausa'}
  </button>
{:else}
  <span class="clip na" title={app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio'}>Portapapeles: {app.clipboard.error ? 'no disponible' : 'solo en escritorio'}</span>
{/if}

<style>
  .clip {
    display: inline-flex; align-items: center; gap: 8px; min-height: 36px; padding: 0 4px; border: 0; background: transparent;
    color: var(--muted); font-size: 12px; cursor: pointer; white-space: nowrap;
  }
  button.clip:hover { color: var(--text); }
  .na { cursor: default; }
  .dot { width: 8px; height: 8px; border-radius: 50%; border: 1.5px solid var(--muted); }
  .dot.on { border-color: var(--accent); background: var(--accent); }
</style>
