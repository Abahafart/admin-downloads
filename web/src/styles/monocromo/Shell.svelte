<script lang="ts">
  // D · Monocromo shell (docs/design/Monocromo.dc.html): left column with
  // the total speed in big type, exit / IP / clipboard as a definition list
  // and the navigation at the bottom; thin line to the main area.
  import { app } from '../../lib/store/app.svelte';
  import { SCREENS } from '../../lib/router.svelte';
  import { splitSpeed } from '../common/model';
  import type { ShellProps } from '../types';
  import './screens.css';
  let { screen, children }: ShellProps = $props();

  const grabber = $derived(app.grabberEntries.length);
  const speed = $derived(splitSpeed(app.totalSpeed));
</script>

<div class="d-shell">
  <aside class="side">
    <span class="brand">ADESC</span>
    <div class="speed">
      <span class="lbl">Velocidad total</span>
      <span class="big tabular">{speed.num}</span>
      <span class="muted">{speed.unit} · {app.counts.activas} {app.counts.activas === 1 ? 'activa' : 'activas'}</span>
    </div>
    <dl class="facts">
      <dt>Salida</dt><dd>{app.currentEgressLabel}</dd>
      <dt>IP</dt><dd class="mono tabular">{app.publicIP || '—'}</dd>
      <dt>Portapapeles</dt>
      <dd>
        {#if app.clipboard.available}
          <button type="button" class="plain" aria-pressed={app.clipboardEnabled} onclick={() => app.setClipboard(!app.clipboardEnabled)}>
            {app.clipboardEnabled ? 'Vigilando' : 'En pausa'}<span class="sr-only">{' '}(pulsa para {app.clipboardEnabled ? 'pausar' : 'vigilar'})</span>
          </button>
        {:else}
          <span class="muted" title={app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio'}>{app.clipboard.error ? 'No disponible' : 'Solo en escritorio'}</span>
        {/if}
      </dd>
      {#if !app.live && app.status === 'ready'}<dt>Conexión</dt><dd><em>Reconectando…</em></dd>{/if}
    </dl>
    <nav aria-label="Principal" class="nav">
      {#each SCREENS as s (s.id)}
        <a href={s.href} aria-current={screen === s.id ? 'page' : undefined} class:on={screen === s.id}>
          {s.label}{#if s.id === 'linkgrabber' && grabber}{` (${grabber})`}{/if}
        </a>
      {/each}
    </nav>
    <div class="help">
      <button type="button" class="plain" aria-keyshortcuts="Control+K" onclick={() => (app.ui.commandOpen = true)}>Comandos <span class="mono">Ctrl K</span></button>
      <button type="button" class="plain" aria-keyshortcuts="?" onclick={() => (app.ui.helpOpen = true)}>Atajos <span class="mono">?</span></button>
    </div>
  </aside>
  <main id="contenido" class="d-main" class:d-shared={screen !== 'descargas'} data-screen={screen}>
    {@render children()}
  </main>
</div>

<style>
  .d-shell { min-height: 100vh; display: flex; flex-wrap: wrap; background: var(--bg); color: var(--text); }
  .side {
    flex: 1 1 300px; max-width: 100%; padding: 40px 40px 32px; display: flex; flex-direction: column; gap: 40px;
    position: sticky; top: 0; align-self: flex-start; height: 100vh; overflow-y: auto;
  }
  .brand { font-weight: 600; letter-spacing: 0.02em; }
  .speed { display: flex; flex-direction: column; gap: 6px; }
  .lbl { font-size: 12px; text-transform: uppercase; letter-spacing: 0.12em; color: var(--muted); }
  .big { font-size: 80px; font-weight: 300; line-height: 1; letter-spacing: -0.04em; }
  .facts { margin: 0; display: grid; grid-template-columns: auto 1fr; gap: 8px 20px; font-size: 13px; align-items: baseline; }
  .facts dt { color: var(--muted); }
  .facts dd { margin: 0; }
  .facts em { font-style: italic; }
  .plain {
    min-height: 36px; margin: -10px 0; padding: 0; border: 0; background: transparent; color: var(--text); cursor: pointer;
    text-decoration: underline; text-decoration-color: var(--border-strong); text-underline-offset: 4px;
  }
  .plain:hover { text-decoration-color: currentColor; }
  .nav { display: flex; flex-direction: column; gap: 2px; margin-top: auto; }
  .nav a { display: flex; align-items: center; min-height: 36px; color: var(--muted); text-decoration: none; }
  .nav a:hover { color: var(--text); }
  .nav a.on { color: var(--text); text-decoration: underline; text-underline-offset: 6px; }
  .help { display: flex; flex-wrap: wrap; gap: 4px 20px; margin-top: -24px; font-size: 12px; }
  .help .plain { color: var(--muted); margin: 0; text-decoration: none; }
  .help .plain:hover { color: var(--text); }
  .d-main {
    flex: 999 1 560px; min-width: 0; padding: 40px 40px 32px; border-left: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 28px;
  }
  @media (max-width: 859px) {
    .side { position: static; height: auto; padding: 24px 16px 16px; gap: 20px; }
    .big { font-size: 56px; }
    .nav { flex-direction: row; flex-wrap: wrap; gap: 0 18px; margin-top: 0; }
    .help { margin-top: -12px; }
    .d-main { border-left: 0; border-top: 1px solid var(--border); padding: 24px 16px 32px; }
  }
</style>
