<script lang="ts">
  // E · Enfoque shell (docs/design/Enfoque.dc.html): calm top text links
  // and a quiet footer with the total speed, the IP and the clipboard.
  import { app } from '../../lib/store/app.svelte';
  import { SCREENS, type ScreenId } from '../../lib/router.svelte';
  import { formatSpeed } from '../../lib/store/format';
  import type { ShellProps } from '../types';
  import './screens.css';
  let { screen, children }: ShellProps = $props();

  const LABEL: Record<ScreenId, string> = { descargas: 'Descargas', linkgrabber: 'LinkGrabber', cuentas: 'Cuentas', red: 'Red', ajustes: 'Ajustes' };
  const grabber = $derived(app.grabberEntries.length);
  const speed = $derived(app.totalSpeed > 0 ? formatSpeed(app.totalSpeed) : '0 MB/s');
</script>

<div class="e-shell">
  <header class="top">
    <span class="brand">adesc</span>
    <nav aria-label="Principal" class="nav">
      {#each SCREENS as s (s.id)}
        <a href={s.href} aria-current={screen === s.id ? 'page' : undefined} class:on={screen === s.id} aria-label={s.id === 'red' ? s.label : undefined}>
          {LABEL[s.id]}{#if s.id === 'linkgrabber' && grabber}{` · ${grabber}`}<span class="sr-only"> pendientes</span>{/if}
        </a>
      {/each}
    </nav>
  </header>
  <main id="contenido" class="e-main" class:e-shared={screen !== 'descargas'} data-screen={screen}>
    {@render children()}
  </main>
  <footer class="foot" class:wide={screen !== 'descargas'} aria-label="Estado de la red">
    <span class="tabular">Total {speed} · {app.currentEgressLabel} · IP {app.publicIP || '—'}{#if !app.live && app.status === 'ready'}<span class="tone-warn">{' '}· reconectando…</span>{/if}</span>
    <span class="end">
      {#if app.clipboard.available}
        <button type="button" class="plain" aria-pressed={app.clipboardEnabled} onclick={() => app.setClipboard(!app.clipboardEnabled)}>{app.clipboardEnabled ? 'Portapapeles activo' : 'Portapapeles en pausa'}</button>
      {:else}
        <span title={app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio'}>Portapapeles: {app.clipboard.error ? 'no disponible' : 'solo en escritorio'}</span>
      {/if}
      <button type="button" class="plain" aria-keyshortcuts="Control+K" onclick={() => (app.ui.commandOpen = true)}>Comandos</button>
      <button type="button" class="plain" aria-keyshortcuts="?" onclick={() => (app.ui.helpOpen = true)}>Atajos</button>
    </span>
  </footer>
</div>

<style>
  .e-shell { min-height: 100vh; display: flex; flex-direction: column; background: var(--bg); color: var(--text); line-height: 1.5; }
  .top { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 24px; padding: 18px 32px; }
  .brand { font-weight: 700; letter-spacing: -0.01em; }
  .nav { display: flex; flex-wrap: wrap; gap: 4px 20px; margin-left: auto; }
  .nav a { display: flex; align-items: center; min-height: 36px; color: var(--muted); text-decoration: none; }
  .nav a:hover { color: var(--text); }
  .nav a.on { color: var(--text); font-weight: 600; }
  .e-main { width: 100%; max-width: 760px; margin: 0 auto; padding: 24px 32px 32px; display: flex; flex-direction: column; gap: 40px; min-width: 0; }
  .e-main.e-shared { max-width: 960px; gap: 28px; }
  .foot {
    width: 100%; max-width: 760px; margin: 0 auto; padding: 0 32px 40px; display: flex; flex-wrap: wrap; justify-content: space-between;
    gap: 8px 16px; color: var(--muted); font-size: 13px;
  }
  .foot.wide { max-width: 960px; }
  .end { display: flex; flex-wrap: wrap; align-items: center; gap: 0 16px; }
  .plain { min-height: 36px; margin: -8px 0; padding: 0; border: 0; background: transparent; color: var(--muted); cursor: pointer; font-size: 13px; }
  .plain:hover { color: var(--text); }
  @media (max-width: 640px) {
    .top { padding: 14px 16px; }
    .nav { margin-left: 0; gap: 0 16px; }
    .e-main { padding: 16px 16px 24px; gap: 32px; }
    .foot { padding: 0 16px 32px; }
  }
</style>
