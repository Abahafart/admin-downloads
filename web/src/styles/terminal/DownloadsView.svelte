<script lang="ts">
  // B · Terminal Descargas (docs/design/Terminal.dc.html): "## cola" heading,
  // monospaced grid with a status glyph column, "█░" character bars, "└"
  // child rows for the files of a folder, inline "r reintentar" and the
  // "!no-range" marker. Wide grid: it scrolls inside its own box.
  import { app } from '../../lib/store/app.svelte';
  import { BUCKETS } from '../../lib/store/status';
  import type { Download } from '../../lib/api/types';
  import {
    charBar, compactBytes, compactSpeed, downloadVM, glyph, itemVM, padPct, shortEgress, termState, type RowVM,
  } from '../common/model';

  const rows = $derived(app.visible.map((it) => itemVM(it, app.now)));
  const hasDrive = $derived(app.accounts.some((a) => a.hoster === 'gdrive' && a.enabled && a.status === 'ok'));
  const q = $derived(app.ui.search.trim());
  const LIVE = new Set(['downloading', 'waiting', 'failed', 'verifying', 'postprocessing', 'resolving']);

  function hoster(h: string): string {
    return h === 'generic' ? 'http' : h || '—';
  }
  function click(key: string) {
    app.select(app.ui.selected === key ? null : key);
  }
  function tone(vm: RowVM): string {
    switch (vm.st.bar) {
      case 'warn':
        return 'warn';
      case 'paused':
      case 'muted':
        return 'paused';
      case 'done':
        return 'done';
      default:
        return 'acc';
    }
  }
  function shownChildren(vm: RowVM, open: boolean): Download[] {
    return open ? vm.children : vm.children.filter((c) => LIVE.has(c.state));
  }
</script>

<div class="head">
  <h1>## cola — {rows.length === app.counts.todas ? '' : `${rows.length} de `}{app.counts.todas} {app.counts.todas === 1 ? 'elemento' : 'elementos'}, {app.counts.activas} {app.counts.activas === 1 ? 'activo' : 'activos'}{#if q}{' · '}buscando «{q}»{/if}</h1>
  <div class="filters" role="toolbar" aria-label="Filtros">
    {#each BUCKETS as b (b.id)}
      <button type="button" aria-pressed={app.ui.filter === b.id} onclick={() => app.setFilter(b.id)}>
        {app.ui.filter === b.id ? `[${b.label.toLowerCase()}` : b.label.toLowerCase()}<span class="n">·{app.counts[b.id]}</span>{app.ui.filter === b.id ? ']' : ''}
      </button>
    {/each}
  </div>
</div>

<div class="scroll">
  <div class="grid" role="table" aria-label="Cola de descargas">
    <div class="r th" role="row">
      <span role="columnheader"><span aria-hidden="true">st</span><span class="sr-only">Estado</span></span>
      <span role="columnheader">nombre</span><span role="columnheader">hoster</span><span role="columnheader">tamaño</span>
      <span role="columnheader">progreso</span><span role="columnheader">vel</span><span role="columnheader">estado / salida</span>
    </div>

    {#each rows as vm (vm.key)}
      {@const open = vm.kind === 'package' && app.isExpanded(vm.packageId)}
      {@render row(vm, false)}
      {#if vm.kind === 'package'}
        {@const kids = shownChildren(vm, open)}
        {#each kids as c (c.id)}
          {@render row(downloadVM(c, app.now), true)}
        {/each}
        {#if !open && kids.length < vm.children.length}
          <div class="r child more" role="row">
            <span role="cell"></span>
            <span role="cell" class="nm">
              <button type="button" class="lnk" aria-expanded="false" onclick={() => app.toggleExpanded(vm.packageId, true)}>{'  └ … '}{vm.children.length - kids.length} más</button>
            </span>
          </div>
        {/if}
      {/if}
    {:else}
      <div class="r empty" role="row">
        <span role="cell"></span>
        <span role="cell" class="nm muted">
          {#if app.items.length === 0}
            (vacía) pega enlaces en › o arrástralos a la ventana
          {:else if q}
            nada coincide con «{q}»
          {:else}
            nada en «{BUCKETS.find((b) => b.id === app.ui.filter)?.label.toLowerCase()}»
          {/if}
        </span>
      </div>
    {/each}
  </div>
</div>

{#snippet row(vm: RowVM, child: boolean)}
  {@const st = vm.st}
  {@const sel = app.isSelected(vm.key)}
  {@const dim = st.state === 'paused' || st.state === 'queued' || st.state === 'completed' || st.state === 'resolving'}
  {@const bar = charBar(vm.pct)}
  {@const eg = shortEgress(vm.egressId, app.data.egress, app.data.rotating)}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions, a11y_interactive_supports_focus -->
  <div
    class="r"
    class:child
    class:dim
    class:sel
    class:pkg={vm.kind === 'package'}
    role="row"
    data-row-key={vm.key}
    onclick={() => click(vm.key)}
    ondblclick={() => vm.kind === 'package' && app.toggleExpanded(vm.packageId)}
  >
    <span role="cell" class="g st-{st.state}"><span aria-hidden="true">{child ? '' : glyph(st.state)}</span></span>
    <span role="cell" class="nm">
      {#if child}
        <span class="tree" aria-hidden="true">{'  └ '}</span><span title={vm.dl?.rel_path || vm.name}>{app.isExpanded(vm.packageId) ? vm.dl?.rel_path || vm.name : vm.name}</span>
      {:else if vm.kind === 'package'}
        <button type="button" class="lnk name" aria-expanded={app.isExpanded(vm.packageId)} onclick={(e) => { e.stopPropagation(); app.toggleExpanded(vm.packageId); }}>
          {vm.name}/ <span class="muted">({vm.filesTotal})</span>
          <span class="sr-only">{app.isExpanded(vm.packageId) ? 'plegar carpeta' : 'desplegar carpeta'}</span>
        </button>
      {:else}
        <span class="name">{vm.name}</span>
      {/if}
      {#if st.notResumable}<span class="muted nr" title="El servidor no admite Range: si se corta, empieza desde cero">{' !no-range'}<span class="sr-only">{' '}(no reanudable)</span></span>{/if}
      {#if sel}<span class="sr-only">{' '}(seleccionada)</span>{/if}
    </span>
    <span role="cell" class="mu">{child ? '' : hoster(vm.hoster)}</span>
    <span role="cell" class="mu">{compactBytes(vm.size)}</span>
    <span role="cell" class="pg">
      {#if st.state === 'failed'}
        <span class="err">{st.fileQuota ? 'cuota del archivo — rotar ip no sirve' : st.detail || 'error'}</span>
      {:else}
        <span role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={vm.pct} aria-label="Progreso de {vm.name}">
          <span aria-hidden="true"><span class="bar {tone(vm)}">{st.state === 'queued' ? '' : bar.fill}</span><span class="rest">{st.state === 'queued' ? bar.fill + bar.empty : bar.empty}</span><span class="pct">{padPct(vm.pct)}</span></span>
        </span>
      {/if}
    </span>
    <span role="cell" class:mu={!vm.speed}>{st.state === 'downloading' ? compactSpeed(vm.speed) : '—'}</span>
    <span role="cell" class="stx">
      {#if st.state === 'failed'}
        <button type="button" class="rbtn" aria-keyshortcuts="r" aria-label="Reintentar {vm.name}" onclick={(e) => { e.stopPropagation(); void app.retry(vm.key); }}><span class="k">r</span> reintentar</button>
      {:else if child && st.state === 'downloading' && vm.connections > 0}
        {vm.connections} seg
      {:else}
        <span class:warn={st.state === 'waiting'}>{termState(st)}{!child && eg && st.state !== 'completed' && st.state !== 'queued' ? ` · ${eg}` : ''}{#if vm.kind === 'package' && vm.st.canRetry}<span class="err">{' '}· {vm.pkg?.files_failed || 1} fallida{(vm.pkg?.files_failed ?? 1) === 1 ? '' : 's'}</span>{/if}</span>
      {/if}
    </span>
  </div>
  {#if st.fileQuota && (st.state === 'failed' || st.state === 'waiting')}
    <div class="r child note" role="row">
      <span role="cell"></span>
      <span role="cell" class="nmw"><span aria-hidden="true">{'  └ '}</span>{#if hasDrive}reintenta: se copia a tu cuenta de drive y se descarga la copia{:else}google drive limita este archivo, no tu ip: <a href="#/cuentas?alta=gdrive">conectar cuenta de drive</a> para copiarlo{/if}
      </span>
    </div>
  {/if}
{/snippet}

<style>
  .head { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 24px; }
  h1 { margin: 0; font-size: 13px; font-weight: 500; color: var(--muted); }
  .filters { display: flex; flex-wrap: wrap; gap: 0 14px; }
  .filters button { min-height: 36px; padding: 0; border: 0; background: transparent; color: var(--muted); cursor: pointer; }
  .filters button:hover { color: var(--text); }
  .filters button[aria-pressed='true'] { color: var(--text); }
  .filters .n { color: var(--muted); }

  .scroll { overflow-x: auto; margin: 0 -4px; padding: 0 4px; }
  .grid { min-width: 900px; display: flex; flex-direction: column; font-variant-numeric: tabular-nums; }
  .r {
    display: grid; grid-template-columns: 28px minmax(0, 2.2fr) 110px 80px minmax(0, 1.6fr) 100px 170px; gap: 16px;
    padding: 8px 0; align-items: baseline;
  }
  .r[data-row-key] { cursor: default; }
  .th { padding: 6px 0; color: var(--muted); border-bottom: 1px dashed var(--border-strong); }
  .child { padding: 2px 0; color: var(--text-2); }
  .child + .r:not(.child) { margin-top: 6px; }
  .dim { color: var(--muted); }
  .dim .name, .dim .lnk.name { color: var(--text); }
  .child.dim { color: var(--muted); }
  .sel { background: var(--raised); box-shadow: inset 2px 0 0 var(--accent); }
  .g { padding-left: 6px; }
  .st-downloading, .st-verifying, .st-postprocessing { color: var(--accent); }
  .st-waiting { color: var(--warn); }
  .st-failed { color: var(--err); }
  .nm { min-width: 0; overflow-wrap: anywhere; }
  .tree { white-space: pre; }
  .mu { color: var(--muted); }
  .pg, .pct { white-space: pre; }
  .bar.acc { color: var(--accent); }
  .bar.warn { color: var(--warn); }
  .bar.paused { color: var(--paused); }
  .bar.done { color: var(--done); }
  .rest { color: var(--track); }
  .err { color: var(--err); white-space: normal; }
  .warn { color: var(--warn); }
  .stx { min-width: 0; }
  .lnk {
    min-height: 36px; margin: -8px 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; cursor: pointer;
    text-align: left; white-space: pre;
  }
  .lnk.name { white-space: normal; overflow-wrap: anywhere; }
  .lnk:hover { text-decoration: underline; }
  .more .lnk { color: var(--muted); }
  .rbtn {
    min-height: 36px; margin: -8px 0; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
    background: transparent; color: var(--text); font: inherit; cursor: pointer;
  }
  .rbtn:hover { background: var(--raised); }
  .rbtn .k { color: var(--accent); }
  .note .nmw { grid-column: 2 / -1; color: var(--text-2); white-space: normal; }
  .note .nmw span { white-space: pre; }
  .note a { color: var(--accent); }
  .empty.r { color: var(--muted); }
  .r.empty .nm { grid-column: 2 / -1; }
  /* Narrow screens: name on one line, bar + state on the next (no inner scroll). */
  @media (max-width: 700px) {
    .grid { min-width: 0; }
    .r { grid-template-columns: 20px auto minmax(0, 1fr); gap: 2px 10px; }
    .r > :nth-child(3), .r > :nth-child(4), .r > :nth-child(6) { display: none; }
    .th { display: none; }
    .r .nm, .r .nmw { grid-column: 2 / -1; }
    .r .pg { grid-column: 2; max-width: 24ch; }
    .r .stx { grid-column: 3; text-align: right; }
    .err { white-space: normal; }
    .g { padding-left: 2px; }
  }
</style>
