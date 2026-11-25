<script lang="ts">
  // A · Grafito shell: sidebar (logo, sections with counts, and at the
  // bottom total speed, exit, IP and the clipboard toggle) + main area.
  import Icon from '../../components/Icon.svelte';
  import LiveStatus from '../../components/LiveStatus.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { SCREENS } from '../../lib/router.svelte';
  import type { ShellProps } from '../types';
  let { screen, children }: ShellProps = $props();
  const grabber = $derived(app.grabberEntries.length);
</script>

<div class="g-shell">
  <nav aria-label="Principal" class="side">
    <div class="brand">
      <Icon name="download" size={20} stroke={1.8} />
      <span>adesc</span>
    </div>
    <div class="links">
      {#each SCREENS as s (s.id)}
        <a href={s.href} aria-current={screen === s.id ? 'page' : undefined} class:on={screen === s.id}>
          <span>{s.label}</span>
          {#if s.id === 'descargas'}
            <span class="n tabular">{app.counts.todas}</span>
          {:else if s.id === 'linkgrabber' && grabber > 0}
            <span class="count-badge tabular" aria-label="{grabber} pendientes">{grabber}</span>
          {/if}
        </a>
      {/each}
    </div>
    <div class="foot">
      <LiveStatus />
      <div class="help">
        <button type="button" class="btn btn-ghost btn-sm" onclick={() => (app.ui.commandOpen = true)} aria-keyshortcuts="Control+K">
          <Icon name="command" size={14} /> Comandos
        </button>
        <button type="button" class="btn btn-ghost btn-sm" onclick={() => (app.ui.helpOpen = true)} aria-keyshortcuts="?">
          <Icon name="help" size={14} /> Atajos <kbd>?</kbd>
        </button>
      </div>
    </div>
  </nav>
  <main class="main" id="contenido">
    {@render children()}
  </main>
</div>

<style>
  .g-shell { min-height: 100vh; display: flex; flex-wrap: wrap; background: var(--bg); color: var(--text); }
  .side {
    flex: 1 1 200px; max-width: 100%; padding: 20px 12px; border-right: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 24px; background: var(--side, var(--bg));
  }
  .brand { display: flex; align-items: center; gap: 10px; padding: 0 8px; font-weight: 600; letter-spacing: -0.01em; }
  .links { display: flex; flex-direction: column; gap: 2px; }
  .links a {
    display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 36px; padding: 0 10px;
    border-radius: var(--radius-md); color: var(--muted); text-decoration: none;
  }
  .links a:hover { color: var(--text); }
  .links a.on { background: var(--raised); color: var(--text); }
  .n { color: var(--muted); }
  .foot { margin-top: auto; display: flex; flex-direction: column; gap: 10px; padding: 12px 10px 0; border-top: 1px solid var(--border); }
  .help { display: flex; flex-wrap: wrap; gap: 2px; margin: 0 -10px; }
  .help .btn { color: var(--muted); font-size: 12px; gap: 6px; }
  .help .btn:hover { color: var(--text); }
  kbd { font-family: var(--font-mono); font-size: 11px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 0 4px; }
  .main { flex: 999 1 560px; min-width: 0; padding: 24px 28px; display: flex; flex-direction: column; gap: 20px; }
  /* Keeps the .sr-only (absolute) cells of wide tables inside their scroll box (no page overflow on phones). */
  .main :global(.table-wrap) { position: relative; }

  @media (max-width: 760px) {
    .side { flex-basis: 100%; border-right: 0; border-bottom: 1px solid var(--border); padding: 12px 16px; gap: 12px; }
    .links { flex-direction: row; overflow-x: auto; gap: 4px; }
    .links a { flex: none; }
    .foot { margin-top: 0; padding-top: 10px; }
    .foot :global(.live.rows) { flex-direction: row; flex-wrap: wrap; gap: 6px 18px; }
    .main { padding: 16px; }
  }
</style>
