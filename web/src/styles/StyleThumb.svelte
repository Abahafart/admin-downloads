<script lang="ts">
  // Built-in thumbnail of a style for Ajustes › Apariencia. The root carries
  // data-estilo + data-esquema, so it is drawn with *that* style's tokens.
  import type { Esquema } from '../theme';
  import type { StyleDefinition } from './types';
  let { style, esquema }: { style: StyleDefinition; esquema: Esquema } = $props();
  const rows = [62, 34, 47, 18, 0];
</script>

<div class="thumb {style.thumb}" data-estilo={style.id} data-esquema={esquema} aria-hidden="true">
  {#if style.thumb === 'sidebar' || style.thumb === 'column-list'}
    <div class="side">
      {#if style.thumb === 'column-list'}<div class="big"></div>{/if}
      <i></i><i></i><i></i>
    </div>
  {:else}
    <div class="top"><i></i><i></i><i></i>{#if style.thumb === 'tabs-cards'}<i></i>{/if}</div>
  {/if}
  <div class="main">
    {#if style.thumb === 'toplinks-focus'}
      <div class="focus"><b style:width="58%"></b></div>
    {/if}
    {#each rows as w, i (i)}
      <div class="row" class:card={style.thumb === 'tabs-cards'}>
        <span class="t"></span>
        <span class="p" class:sq={style.thumb === 'toplinks-bar'}><b style:width="{w}%" class:warn={i === 2}></b></span>
      </div>
    {/each}
  </div>
  {#if style.thumb === 'toplinks-bar'}<div class="bottom"></div>{/if}
</div>

<style>
  .thumb {
    position: relative; display: flex; width: 100%; aspect-ratio: 16 / 10; overflow: hidden;
    background: var(--bg); color: var(--text); border-radius: var(--radius-sm); font-family: var(--font);
  }
  .toplinks-bar, .tabs-cards, .toplinks-focus { flex-direction: column; }
  .side { flex: 0 0 26%; display: flex; flex-direction: column; gap: 5px; padding: 8px 6px; border-right: 1px solid var(--border); background: var(--side, var(--surface)); }
  .column-list .side { background: var(--bg); }
  .side i, .top i { display: block; height: 4px; border-radius: 2px; background: var(--muted); opacity: 0.6; }
  .side i:first-of-type, .top i:first-child { background: var(--text); opacity: 1; }
  .big { height: 14px; width: 70%; background: var(--text); margin-bottom: 4px; }
  .top { display: flex; gap: 6px; padding: 6px 8px; border-bottom: 1px solid var(--border); }
  .top i { width: 14%; }
  .main { flex: 1; display: flex; flex-direction: column; gap: 5px; padding: 8px; min-width: 0; }
  .row { display: flex; align-items: center; gap: 6px; }
  .row.card { padding: 3px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
  .t { flex: 0 0 34%; height: 4px; border-radius: 2px; background: var(--text-2); }
  .p { flex: 1; height: 3px; border-radius: 2px; background: var(--track); overflow: hidden; }
  .p b { display: block; height: 100%; background: var(--accent); }
  .p b.warn { background: var(--warn); }
  .p.sq { height: 4px; border-radius: 0; }
  .focus { height: 18px; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 6px; margin-bottom: 2px; background: var(--surface); }
  .focus b { display: block; height: 3px; background: var(--accent); }
  .bottom { height: 8px; border-top: 1px solid var(--border); background: var(--surface); }
</style>
