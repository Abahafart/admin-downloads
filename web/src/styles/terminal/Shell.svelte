<script lang="ts">
  // B · Terminal shell (docs/design/Terminal.dc.html): text links on top
  // ("[descargas]"), total speed + exit + IP on the right, the "›" prompt and
  // the always-visible shortcut bar at the bottom with the clipboard switch.
  import Prompt from './Prompt.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { SCREENS, type ScreenId } from '../../lib/router.svelte';
  import { formatSpeed } from '../../lib/store/format';
  import { shortEgress } from '../common/model';
  import type { ShellProps } from '../types';
  import './screens.css';
  let { screen, children }: ShellProps = $props();
  let footH = $state(40);

  const LABEL: Record<ScreenId, string> = { descargas: 'descargas', linkgrabber: 'linkgrabber', cuentas: 'cuentas', red: 'red', ajustes: 'ajustes' };
  const grabber = $derived(app.grabberEntries.length);
  const speed = $derived((app.totalSpeed > 0 ? formatSpeed(app.totalSpeed) : '0 MB/s').replace(',', '.'));
  const exit = $derived(shortEgress(app.currentEgress || 'direct', app.data.egress, app.data.rotating) || 'directa');

  function needSel(fn: (key: string) => unknown) {
    const k = app.ui.selected;
    if (!k) app.toast('Selecciona una descarga primero');
    else void fn(k);
  }
  function focusSearch() {
    document.querySelector<HTMLElement>('[data-shortcut="search"]')?.focus();
  }
</script>

<div class="t-shell" style:--t-foot="{footH}px">
  <nav aria-label="Principal" class="top">
    <span class="brand">adesc</span>
    <div class="links">
      {#each SCREENS as s (s.id)}
        <a href={s.href} aria-current={screen === s.id ? 'page' : undefined} class:on={screen === s.id} aria-label={s.id === 'linkgrabber' && grabber ? `${s.label}, ${grabber} pendientes` : s.label}>
          {screen === s.id ? `[${LABEL[s.id]}]` : LABEL[s.id]}{#if s.id === 'linkgrabber' && grabber}·{grabber}{/if}
        </a>
      {/each}
    </div>
    <p class="live" aria-label="Estado de la red">
      <span aria-hidden="true">{'↓ '}</span><span class="sr-only">Velocidad total </span><span class="v tabular">{speed}</span><span aria-hidden="true">{' · '}</span><span class="sr-only">, salida </span>{exit}<span class="sr-only">, IP</span>{' '}<span class="tabular">{app.publicIP || '—'}</span>{#if !app.live && app.status === 'ready'}<span class="tone-warn">{' · reconectando…'}</span>{/if}
    </p>
  </nav>

  <div class="pr"><Prompt /></div>

  <main id="contenido" class="t-main" class:t-shared={screen !== 'descargas'} data-screen={screen}>
    {@render children()}
  </main>

  <footer class="keys" aria-label="Atajos de teclado" bind:clientHeight={footH}>
    <button type="button" aria-keyshortcuts="p" onclick={() => app.togglePause()}><span class="k">p</span> pausar</button>
    <button type="button" aria-keyshortcuts="r" onclick={() => needSel((k) => app.retry(k))}><span class="k">r</span> reintentar</button>
    <button type="button" aria-keyshortcuts="Shift+R" onclick={() => needSel((k) => app.restart(k))}><span class="k">R</span> reiniciar</button>
    <button type="button" aria-keyshortcuts="i" onclick={() => app.rotate()}><span class="k">i</span> rotar ip</button>
    <button type="button" aria-keyshortcuts="/" onclick={focusSearch}><span class="k">/</span> buscar</button>
    <button type="button" aria-keyshortcuts="Delete" onclick={() => needSel((k) => app.askDelete(k))}><span class="k">supr</span> borrar</button>
    <button type="button" aria-keyshortcuts="?" onclick={() => (app.ui.helpOpen = true)}><span class="k">?</span> ayuda</button>
    <span class="clip">
      {#if app.clipboard.available}
        <button type="button" aria-pressed={app.clipboardEnabled} onclick={() => app.setClipboard(!app.clipboardEnabled)}>
          portapapeles: <span class:on={app.clipboardEnabled}>{app.clipboardEnabled ? 'on' : 'off'}</span>
        </button>
      {:else}
        <span class="na" title={app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio'}>portapapeles: {app.clipboard.error ? 'n/d' : 'solo escritorio'}</span>
      {/if}
    </span>
  </footer>
</div>

<style>
  .t-shell { min-height: 100vh; display: flex; flex-direction: column; background: var(--bg); color: var(--text); line-height: 1.6; }
  .top { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 20px; padding: 14px 28px; border-bottom: 1px solid var(--border); }
  .brand { font-weight: 700; color: var(--accent); margin-right: 12px; }
  .links { display: flex; flex-wrap: wrap; gap: 0 20px; }
  .links a { display: flex; align-items: center; min-height: 36px; color: var(--muted); text-decoration: none; }
  .links a:hover { color: var(--text); text-decoration: underline; }
  .links a.on { color: var(--text); }
  .live { margin: 0 0 0 auto; color: var(--muted); }
  .live .v { color: var(--text); }
  .pr { padding: 24px 28px 0; }
  .t-main { flex: 1; min-width: 0; padding: 20px 28px; display: flex; flex-direction: column; gap: 16px; }
  .keys {
    position: sticky; bottom: 0; z-index: 5; display: flex; flex-wrap: wrap; align-items: center; gap: 0 24px; padding: 2px 28px;
    border-top: 1px solid var(--border); background: var(--bg); color: var(--muted); font-size: 12px;
  }
  .keys button {
    min-height: 36px; padding: 0; border: 0; background: transparent; color: var(--muted); font: inherit; cursor: pointer;
  }
  .keys button:hover { color: var(--text); }
  .k { color: var(--text); }
  .clip { margin-left: auto; display: flex; align-items: center; min-height: 36px; }
  .clip .on { color: var(--accent); }
  @media (max-width: 760px) {
    .top { padding: 10px 16px; }
    .live { margin-left: 0; flex-basis: 100%; }
    .pr { padding: 16px 16px 0; }
    .t-main { padding: 16px; }
    .keys { padding: 2px 16px; gap: 0 16px; }
    .clip { margin-left: 0; }
  }
</style>
