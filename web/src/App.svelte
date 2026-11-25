<script lang="ts">
  // Root: picks the active style from the registry and renders its Shell
  // with the current screen. Shared overlays (command bar, help, delete
  // confirmation, toasts) and the locked-vault banner live here so every
  // style gets them.
  import { app } from './lib/store/app.svelte';
  import { router } from './lib/router.svelte';
  import { isTypingTarget } from './lib/keyboard';
  import { hasLinks } from './lib/commands';
  import { appearance } from './theme/runtime.svelte';
  import { getStyle } from './styles/registry';
  import { screenFor } from './screens';
  import Login from './components/Login.svelte';
  import ConnectionScreen from './components/ConnectionScreen.svelte';
  import CommandBar from './components/CommandBar.svelte';
  import HelpOverlay from './components/HelpOverlay.svelte';
  import ConfirmDelete from './components/ConfirmDelete.svelte';
  import Toasts from './components/Toasts.svelte';
  import VaultBanner from './components/VaultBanner.svelte';

  const style = $derived(getStyle(appearance.pref.estilo));
  const Shell = $derived(style.Shell);
  const Screen = $derived(screenFor(style, router.screen));
  const Cmd = $derived(style.CommandBar ?? CommandBar);
  const Help = $derived(style.HelpOverlay ?? HelpOverlay);
  let dragging = $state(false);
  let everReady = $state(false);
  $effect(() => {
    if (app.status === 'ready') everReady = true;
  });

  async function drop(e: DragEvent) {
    dragging = false;
    // In the desktop app its injected script handles window drops (it also
    // imports WireGuard .conf files).
    if (e.defaultPrevented || app.status !== 'ready' || app.isDesktop) return;
    e.preventDefault();
    const dt = e.dataTransfer;
    let t = dt?.getData('text/uri-list') || dt?.getData('text/plain') || '';
    if (!t && dt?.files.length) t = (await Promise.all([...dt.files].map((f) => f.text()))).join('\n');
    if (t) await app.addLinks(t);
  }

  function paste(e: ClipboardEvent) {
    if (app.status !== 'ready' || isTypingTarget(e.target) || app.ui.commandOpen) return;
    const t = e.clipboardData?.getData('text') ?? '';
    if (hasLinks(t)) {
      e.preventDefault();
      void app.addLinks(t);
    }
  }
</script>

<svelte:window
  ondragover={(e) => {
    if (app.status === 'ready' && !app.isDesktop && e.dataTransfer?.types.some((t) => t === 'text/plain' || t === 'text/uri-list' || t === 'Files')) {
      e.preventDefault();
      dragging = true;
    }
  }}
  ondragleave={(e) => {
    if (!e.relatedTarget) dragging = false;
  }}
  ondrop={drop}
  onpaste={paste}
/>

<a class="skip sr-only" href="#contenido">Saltar al contenido</a>

{#if app.status === 'login'}
  <Login />
{:else if app.status === 'ready' || everReady}
  <Shell screen={router.screen}>
    <!-- Cuentas renders its own, fuller banner (first unlock, warning). -->
    {#if router.screen !== 'cuentas'}<VaultBanner />{/if}
    <Screen />
  </Shell>
{:else}
  <ConnectionScreen />
{/if}

<Cmd />
<Help />
<ConfirmDelete />
<Toasts />

{#if dragging}
  <div class="drop" aria-hidden="true"><p>Suelta para añadir los enlaces</p></div>
{/if}

<style>
  .skip:focus { position: fixed !important; left: 8px; top: 8px; width: auto; height: auto; clip: auto; padding: 8px 12px; z-index: 100; background: var(--raised); color: var(--text); border-radius: var(--radius-md); }
  .drop {
    position: fixed; inset: 0; z-index: 80; display: grid; place-items: center; pointer-events: none;
    border: 2px dashed var(--accent); background: var(--bg); opacity: 0.9;
  }
  .drop p { font-size: 18px; font-weight: 600; }
</style>
