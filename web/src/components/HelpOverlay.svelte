<script lang="ts">
  import Modal from './Modal.svelte';
  import { app } from '../lib/store/app.svelte';
  import { SHORTCUTS } from '../lib/keyboard';
  import { COMMANDS } from '../lib/commands';
</script>

<Modal open={app.ui.helpOpen} title="Atajos de teclado" onclose={() => (app.ui.helpOpen = false)}>
  <p class="hint">Actúan sobre la descarga seleccionada y no se activan mientras escribes en un campo.</p>
  <dl class="keys">
    {#each SHORTCUTS as s (s.label)}
      <dt>{#each s.keys as k, i (k)}{#if i}<span class="muted"> </span>{/if}<kbd>{k}</kbd>{/each}</dt>
      <dd>{s.label}</dd>
    {/each}
  </dl>
  <h3>Comandos (Ctrl+K)</h3>
  <dl class="keys">
    {#each COMMANDS.slice(0, 4) as c (c.name)}
      <dt><code class="mono">{c.name}</code></dt>
      <dd>{c.label}</dd>
    {/each}
    <dt><span class="muted">pegar enlaces</span></dt>
    <dd>Añadirlos al LinkGrabber</dd>
  </dl>
  <div><button type="button" class="btn" onclick={() => (app.ui.helpOpen = false)}>Cerrar</button></div>
</Modal>

<style>
  .keys { display: grid; grid-template-columns: max-content 1fr; gap: 8px 16px; margin: 0; align-items: center; }
  dd { margin: 0; color: var(--text-2); }
  kbd {
    display: inline-block; min-width: 24px; padding: 1px 6px; border: 1px solid var(--border-strong); border-bottom-width: 2px;
    border-radius: var(--radius-sm); font-family: var(--font-mono); font-size: 12px; text-align: center; background: var(--raised);
  }
  h3 { margin: 6px 0 0; font-size: 13px; font-weight: 600; }
</style>
