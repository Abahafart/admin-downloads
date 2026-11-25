<script lang="ts">
  // Ajustes › Apariencia thumbnail of Terminal: text nav, "›" prompt, glyph
  // rows with "█░" bars and the shortcut bar. Drawn with Terminal's tokens
  // for the given scheme (data-estilo + data-esquema on the root).
  import type { Esquema } from '../../theme';
  let { esquema }: { esquema: Esquema } = $props();
  const rows: [string, string, number, string][] = [
    ['▸', 'acc', 7, 'Curso/'],
    ['▸', 'acc', 4, 'dataset'],
    ['◷', 'warn', 5, 'Album'],
    ['‖', 'paused', 2, 'backup'],
    ['✓', 'done', 11, 'manual'],
  ];
</script>

<div class="thumb" data-estilo="terminal" data-esquema={esquema} aria-hidden="true">
  <div class="nav"><b>adesc</b> [desc] link cuen red</div>
  <div class="pr"><span class="acc">›</span> pega enlaces</div>
  <div class="rows">
    {#each rows as [g, t, n, name] (name)}
      <div class="r"><span class={t}>{g}</span><span class="nm">{name}</span><span><span class={t}>{'█'.repeat(n)}</span><span class="e">{'░'.repeat(11 - n)}</span></span></div>
    {/each}
  </div>
  <div class="ft">p pausar · r reintentar · i rotar</div>
</div>

<style>
  .thumb {
    display: flex; flex-direction: column; width: 100%; aspect-ratio: 16 / 10; overflow: hidden;
    background: var(--bg); color: var(--text); font-family: var(--font-mono); font-size: 7px; line-height: 1.5; white-space: pre;
  }
  .nav { padding: 4px 6px; border-bottom: 1px solid var(--border); color: var(--muted); }
  .nav b { color: var(--accent); }
  .pr { margin: 5px 6px 2px; padding: 2px 4px; border: 1px solid var(--border-strong); background: var(--surface); color: var(--muted); }
  .rows { flex: 1; padding: 3px 6px; display: flex; flex-direction: column; gap: 1px; }
  .r { display: grid; grid-template-columns: 8px 1fr auto; gap: 4px; }
  .nm { overflow: hidden; }
  .acc { color: var(--accent); }
  .warn { color: var(--warn); }
  .paused { color: var(--paused); }
  .done { color: var(--done); }
  .e { color: var(--track); }
  .ft { padding: 2px 6px; border-top: 1px solid var(--border); color: var(--muted); }
</style>
