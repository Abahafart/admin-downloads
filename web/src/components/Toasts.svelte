<script lang="ts">
  import { app } from '../lib/store/app.svelte';
  import Icon from './Icon.svelte';
  const ICON = { info: 'info', success: 'check', warning: 'alert', error: 'alert' } as const;
  const WORD = { info: 'Aviso', success: 'Hecho', warning: 'Atención', error: 'Error' } as const;
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each app.toasts as t (t.id)}
    <div class="toast {t.level}">
      <span class="ic tone-{t.level === 'error' ? 'err' : t.level === 'warning' ? 'warn' : 'muted'}"><Icon name={ICON[t.level]} /></span>
      <p><span class="sr-only">{WORD[t.level]}: </span>{t.message}</p>
      <button type="button" class="btn btn-ghost btn-icon" aria-label="Cerrar aviso" onclick={() => app.dismiss(t.id)}><Icon name="x" /></button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed; right: 16px; bottom: 16px; z-index: 60; display: flex; flex-direction: column; gap: 8px;
    width: min(420px, calc(100vw - 32px)); pointer-events: none;
  }
  .toast {
    pointer-events: auto; display: flex; align-items: center; gap: 10px; padding: 4px 4px 4px 12px;
    border: 1px solid var(--border-strong); border-radius: var(--radius-md); background: var(--raised); color: var(--text);
  }
  .toast.error { border-color: var(--err); }
  .toast.warning { border-color: var(--warn); }
  .ic { display: inline-flex; }
  p { margin: 0; flex: 1; padding: 6px 0; }
</style>
