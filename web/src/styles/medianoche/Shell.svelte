<script lang="ts">
  // C · Medianoche shell (docs/design/Medianoche.dc.html): centered
  // container, pill tabs with the LinkGrabber count, total speed + exit · IP
  // on the right. The clipboard switch lives in the Descargas drop field and
  // in this header on the other screens.
  import ClipToggle from './ClipToggle.svelte';
  import Icon from '../../components/Icon.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { SCREENS, type ScreenId } from '../../lib/router.svelte';
  import { splitSpeed } from '../common/model';
  import type { ShellProps } from '../types';
  import './screens.css';
  let { screen, children }: ShellProps = $props();

  const LABEL: Record<ScreenId, string> = { descargas: 'Descargas', linkgrabber: 'LinkGrabber', cuentas: 'Cuentas', red: 'Red', ajustes: 'Ajustes' };
  const grabber = $derived(app.grabberEntries.length);
  const speed = $derived(splitSpeed(app.totalSpeed));
</script>

<div class="m-shell">
  <div class="wrap">
    <header class="top">
      <span class="brand">adesc</span>
      <nav aria-label="Principal" class="tabs">
        {#each SCREENS as s (s.id)}
          <a href={s.href} aria-current={screen === s.id ? 'page' : undefined} class:on={screen === s.id} aria-label={s.id === 'red' ? s.label : undefined}>
            {LABEL[s.id]}
            {#if s.id === 'linkgrabber' && grabber}<span class="n tabular">{grabber}</span><span class="sr-only"> pendientes</span>{/if}
          </a>
        {/each}
      </nav>
      <div class="live" aria-label="Estado de la red">
        <span><span class="sr-only">Velocidad total </span><b class="tabular">{speed.num}</b> {speed.unit}</span>
        <span><span class="sr-only">Salida </span>{app.currentEgressLabel} · <span class="sr-only">IP </span><span class="tabular">{app.publicIP || '—'}</span></span>
        {#if !app.live && app.status === 'ready'}<span class="tone-warn">Reconectando…</span>{/if}
        {#if screen !== 'descargas'}<ClipToggle />{/if}
        <button type="button" class="ic" aria-label="Barra de comandos" aria-keyshortcuts="Control+K" title="Comandos (Ctrl+K)" onclick={() => (app.ui.commandOpen = true)}><Icon name="command" size={16} /></button>
        <button type="button" class="ic" aria-label="Atajos de teclado" aria-keyshortcuts="?" title="Atajos (?)" onclick={() => (app.ui.helpOpen = true)}><Icon name="help" size={16} /></button>
      </div>
    </header>
    <main id="contenido" class="m-main" class:m-shared={screen !== 'descargas'} data-screen={screen}>
      {@render children()}
    </main>
  </div>
</div>

<style>
  .m-shell { min-height: 100vh; background: var(--bg); color: var(--text); }
  .wrap { max-width: 1180px; margin: 0 auto; padding: 24px 32px 40px; display: flex; flex-direction: column; gap: 24px; }
  .top { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 16px; }
  .brand { font-weight: 600; font-size: 16px; letter-spacing: -0.01em; margin-right: 8px; }
  .tabs { display: flex; flex-wrap: wrap; gap: 4px; padding: 4px; border-radius: var(--radius-md); background: var(--surface); border: 1px solid var(--border); }
  .tabs a {
    display: flex; align-items: center; gap: 6px; min-height: 36px; padding: 0 14px; border-radius: calc(var(--radius-md) - 3px);
    color: var(--muted); text-decoration: none;
  }
  .tabs a:hover { color: var(--text); }
  .tabs a.on { background: var(--raised); color: var(--text); font-weight: 500; }
  .n { color: var(--accent); font-weight: 600; }
  .live { margin-left: auto; display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; color: var(--muted); }
  .live b { color: var(--text); font-weight: 600; }
  .ic {
    width: 36px; height: 36px; display: inline-flex; align-items: center; justify-content: center; margin: 0 -8px 0 -4px;
    border: 0; border-radius: var(--radius-pill); background: transparent; color: var(--muted); cursor: pointer;
  }
  .ic:hover { color: var(--text); background: var(--surface); }
  .m-main { display: flex; flex-direction: column; gap: 24px; min-width: 0; }
  @media (max-width: 760px) {
    .wrap { padding: 16px 16px 32px; gap: 20px; }
    .tabs { width: 100%; flex-wrap: nowrap; overflow-x: auto; }
    .tabs a { flex: none; padding: 0 10px; }
    .live { margin-left: 0; }
  }
</style>
