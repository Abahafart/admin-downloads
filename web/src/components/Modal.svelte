<script lang="ts">
  // Minimal accessible dialog on <dialog> (focus handling and Esc for free).
  import type { Snippet } from 'svelte';
  let {
    open,
    title,
    onclose,
    children,
    width = 560,
    labelledby,
  }: { open: boolean; title: string; onclose: () => void; children: Snippet; width?: number; labelledby?: string } = $props();
  let dlg: HTMLDialogElement | undefined = $state();
  const hid = `dlg-${Math.random().toString(36).slice(2, 8)}`;

  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    else if (!open && dlg.open) dlg.close();
  });
</script>

<dialog
  bind:this={dlg}
  aria-labelledby={labelledby ?? hid}
  style:--w="{width}px"
  onclose={() => open && onclose()}
  onclick={(e) => {
    if (e.target === dlg) onclose();
  }}
>
  {#if open}
    <div class="inner">
      <h2 id={hid} class={labelledby ? 'sr-only' : ''}>{title}</h2>
      {@render children()}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    width: min(var(--w), calc(100vw - 32px)); max-height: calc(100vh - 64px); padding: 0;
    border: 1px solid var(--border-strong); border-radius: var(--radius-lg); background: var(--surface); color: var(--text);
  }
  dialog::backdrop { background: var(--bg); opacity: 0.7; }
  .inner { padding: 20px; display: flex; flex-direction: column; gap: 14px; }
  h2 { margin: 0; font-size: 16px; font-weight: 600; }
</style>
