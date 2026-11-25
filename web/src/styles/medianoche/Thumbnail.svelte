<script lang="ts">
  // Ajustes › Apariencia thumbnail of Medianoche: pill tabs, dashed drop
  // field and a grid of cards with thick bars. Drawn with Medianoche's
  // tokens for the given scheme.
  import type { Esquema } from '../../theme';
  let { esquema }: { esquema: Esquema } = $props();
  const cards: [number, string][] = [[62, 'acc'], [34, 'acc'], [47, 'warn'], [18, 'paused']];
</script>

<div class="thumb" data-estilo="medianoche" data-esquema={esquema} aria-hidden="true">
  <div class="top"><span class="brand"></span><span class="tabs"><i class="on"></i><i></i><i></i><i></i></span></div>
  <div class="drop"></div>
  <div class="grid">
    {#each cards as [w, t], i (i)}
      <div class="card" class:quiet={t === 'paused'}>
        <span class="t"></span>
        <span class="bar"><b class={t} style:width="{w}%"></b></span>
      </div>
    {/each}
  </div>
</div>

<style>
  .thumb {
    display: flex; flex-direction: column; gap: 5px; width: 100%; aspect-ratio: 16 / 10; overflow: hidden; padding: 6px 10px;
    background: var(--bg); color: var(--text); border-radius: var(--radius-sm);
  }
  .top { display: flex; align-items: center; gap: 6px; }
  .brand { width: 14px; height: 4px; border-radius: var(--radius-sm); background: var(--text); }
  .tabs { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
  .tabs i { width: 14px; height: 6px; border-radius: var(--radius-sm); background: var(--muted); opacity: 0.45; }
  .tabs i.on { background: var(--raised); opacity: 1; box-shadow: inset 0 0 0 1px var(--border-strong); }
  .drop { height: 10px; border: 1px dashed var(--border-strong); border-radius: var(--radius-sm); }
  .grid { flex: 1; display: grid; grid-template-columns: 1fr 1fr; gap: 5px; }
  .card { display: flex; flex-direction: column; justify-content: space-between; padding: 5px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
  .card.quiet { background: transparent; }
  .t { width: 60%; height: 4px; border-radius: var(--radius-sm); background: var(--text-2); }
  .bar { height: 3px; border-radius: var(--radius-sm); background: var(--track); overflow: hidden; }
  .bar b { display: block; height: 100%; background: var(--accent); }
  .bar b.warn { background: var(--warn); }
  .bar b.paused { background: var(--paused); }
</style>
