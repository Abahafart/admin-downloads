<script lang="ts">
  // Ajustes › Apariencia thumbnail of Monocromo: left column with the big
  // speed, thin-line list with 2 px fills (dashed for a quota wait) and a
  // struck-through completed line. No state colors, as in the style.
  import type { Esquema } from '../../theme';
  let { esquema }: { esquema: Esquema } = $props();
  const lines: [number, string][] = [[62, 'acc'], [34, 'acc'], [47, 'dash'], [18, 'still'], [0, 'done']];
</script>

<div class="thumb" data-estilo="monocromo" data-esquema={esquema} aria-hidden="true">
  <div class="side">
    <span class="lbl"></span>
    <span class="big">24,5</span>
    <i></i><i></i><i></i>
  </div>
  <div class="main">
    {#each lines as [w, t], i (i)}
      <div class="ln">
        <span class="nm" class:done={t === 'done'}></span>
        {#if t !== 'done'}<span class="tr"><b class={t} style:width="{w}%"></b></span>{/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .thumb { display: flex; width: 100%; aspect-ratio: 16 / 10; overflow: hidden; background: var(--bg); color: var(--text); font-family: var(--font); }
  .side { flex: 0 0 34%; display: flex; flex-direction: column; gap: 4px; padding: 10px 8px; }
  .lbl { width: 50%; height: 2px; background: var(--muted); }
  .big { font-size: 18px; font-weight: 300; letter-spacing: -0.04em; line-height: 1; margin-bottom: 6px; }
  .side i { display: block; width: 60%; height: 2px; background: var(--muted); margin-top: 3px; }
  .main { flex: 1; display: flex; flex-direction: column; padding: 8px 8px 8px 10px; border-left: 1px solid var(--border); }
  .ln { flex: 1; display: flex; flex-direction: column; justify-content: center; gap: 4px; border-bottom: 1px solid var(--border); }
  .ln:last-child { border-bottom: 0; }
  .nm { width: 55%; height: 3px; background: var(--text); }
  .nm.done { background: var(--muted); height: 1px; margin-top: 2px; }
  .tr { height: 1px; background: var(--border-strong); }
  .tr b { display: block; height: 2px; margin-top: -0.5px; background: var(--accent); }
  .tr b.dash { background: repeating-linear-gradient(90deg, var(--text) 0 3px, transparent 3px 6px); }
  .tr b.still { background: var(--muted); }
</style>
