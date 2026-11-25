<script lang="ts">
  // Ajustes › Apariencia thumbnail of Enfoque: links on the right, the
  // active download big (percent + thick bar) and the quiet "Después" list.
  import type { Esquema } from '../../theme';
  let { esquema }: { esquema: Esquema } = $props();
  const next: [number, string][] = [[34, 'acc'], [47, 'warn'], [18, 'paused']];
</script>

<div class="thumb" data-estilo="enfoque" data-esquema={esquema} aria-hidden="true">
  <div class="top"><i class="on"></i><i></i><i></i><i></i></div>
  <div class="main">
    <div class="now"><span class="t"></span><span class="pct">62%</span></div>
    <span class="bar"><b style:width="62%"></b></span>
    <div class="list">
      {#each next as [w, t], i (i)}
        <div class="ln"><span class="n"></span><span class="mini"><b class={t} style:width="{w}%"></b></span></div>
      {/each}
    </div>
  </div>
</div>

<style>
  .thumb { display: flex; flex-direction: column; width: 100%; aspect-ratio: 16 / 10; overflow: hidden; background: var(--bg); color: var(--text); font-family: var(--font); }
  .top { display: flex; justify-content: flex-end; gap: 5px; padding: 6px 10px; }
  .top i { width: 12px; height: 3px; border-radius: var(--radius-pill); background: var(--muted); opacity: 0.6; }
  .top i.on { background: var(--text); opacity: 1; }
  .main { flex: 1; width: 74%; margin: 0 auto; display: flex; flex-direction: column; gap: 5px; padding-top: 4px; }
  .now { display: flex; align-items: flex-end; justify-content: space-between; }
  .t { width: 46%; height: 6px; border-radius: var(--radius-pill); background: var(--text); }
  .pct { font-size: 13px; font-weight: 500; line-height: 1; color: var(--accent); letter-spacing: -0.03em; }
  .bar { height: 4px; border-radius: var(--radius-pill); background: var(--track); overflow: hidden; }
  .bar b, .mini b { display: block; height: 100%; background: var(--accent); }
  .list { display: flex; flex-direction: column; margin-top: 4px; }
  .ln { display: flex; align-items: center; justify-content: space-between; gap: 6px; padding: 4px 0; border-bottom: 1px solid var(--border); }
  .ln:last-child { border-bottom: 0; }
  .n { width: 40%; height: 3px; border-radius: var(--radius-pill); background: var(--text-2); }
  .mini { width: 26%; height: 2px; border-radius: var(--radius-pill); background: var(--track); overflow: hidden; }
  .mini b.warn { background: var(--warn); }
  .mini b.paused { background: var(--paused); }
</style>
