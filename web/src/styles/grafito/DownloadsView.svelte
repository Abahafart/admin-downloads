<script lang="ts">
  // A · Grafito Descargas: header with search, "Pausar todo" and "Añadir
  // enlaces", pill filters, table with expandable folder packages
  // (docs/design/Grafito.dc.html).
  import AddLinks from '../../components/AddLinks.svelte';
  import DriveQuotaHelp from '../../components/DriveQuotaHelp.svelte';
  import EgressLabel from '../../components/EgressLabel.svelte';
  import Icon from '../../components/Icon.svelte';
  import NotResumable from '../../components/NotResumable.svelte';
  import PackageToggle from '../../components/PackageToggle.svelte';
  import ProgressBar from '../../components/ProgressBar.svelte';
  import RetryButton from '../../components/RetryButton.svelte';
  import StateLabel from '../../components/StateLabel.svelte';
  import SelectionBar from './SelectionBar.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { formatBytes, formatSpeed, percent, plural } from '../../lib/store/format';
  import { hosterName } from '../../lib/store/labels';
  import { aggregate, dkey } from '../../lib/store/rows';
  import { BUCKETS, packageStatusOf, statusOf } from '../../lib/store/status';
  import type { Download } from '../../lib/api/types';

  let adding = $state(false);
  const bucketLabel = $derived(BUCKETS.find((b) => b.id === app.ui.filter)?.label ?? '');

  function rowClick(key: string) {
    app.select(app.ui.selected === key ? null : key);
  }
</script>

<header class="head">
  <h1>Descargas</h1>
  <label class="search">
    <Icon name="search" size={14} />
    <span class="sr-only">Buscar</span>
    <input type="search" placeholder="Buscar archivo o hoster" bind:value={app.ui.search} data-shortcut="search" aria-keyshortcuts="/" />
  </label>
  <button type="button" class="btn" onclick={() => app.togglePauseAll()}>{app.data.stats.paused ? 'Reanudar todo' : 'Pausar todo'}</button>
  <button type="button" class="btn btn-primary add-btn" aria-expanded={adding} aria-controls="g-add" onclick={() => (adding = !adding)}>Añadir enlaces</button>
</header>

{#if adding}
  <section id="g-add" class="panel addp" aria-label="Añadir enlaces">
    <AddLinks autofocus onadded={() => (adding = false)} id="g-add-links" />
  </section>
{/if}

<div class="bar">
  <div role="toolbar" aria-label="Filtros" class="pills">
    {#each BUCKETS as b (b.id)}
      <button type="button" class="pill" aria-pressed={app.ui.filter === b.id} onclick={() => app.setFilter(b.id)}>
        {b.label} <span class="tabular">{app.counts[b.id]}</span>
      </button>
    {/each}
  </div>
  <SelectionBar />
</div>

<div class="table-wrap">
  <table class="table dl">
    <thead>
      <tr>
        <th scope="col">Nombre</th>
        <th scope="col">Hoster</th>
        <th scope="col">Tamaño</th>
        <th scope="col" class="w-prog">Progreso</th>
        <th scope="col">Velocidad</th>
        <th scope="col">Estado</th>
        <th scope="col">Salida</th>
      </tr>
    </thead>
    <tbody>
      {#each app.visible as it (it.key)}
        {#if it.kind === 'package'}
          {@const ag = aggregate(it.pkg, it.children)}
          {@const st = packageStatusOf({ ...it.pkg, state: it.pkg.state, eta: ag.eta, speed: ag.speed }, it.children, app.now)}
          {@const open = app.isExpanded(it.pkg.id)}
          <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
          <tr data-row-key={it.key} class="pkg" class:sel={app.isSelected(it.key)} onclick={() => rowClick(it.key)} ondblclick={() => app.toggleExpanded(it.pkg.id)}>
            <td>
              <div class="name">
                <PackageToggle packageId={it.pkg.id} name="{it.pkg.name}/" />
                <span class="muted nowrap">{plural(ag.filesTotal, 'archivo', 'archivos')}</span>
                {#if st.notResumable}<NotResumable />{/if}
                {#if app.isSelected(it.key)}<span class="sr-only">(seleccionada)</span>{/if}
              </div>
            </td>
            <td class="muted">{hosterName(it.pkg.hoster)}</td>
            <td class="muted">{formatBytes(ag.size)}</td>
            <td>{@render progress(it.pkg.name, percent(ag.done, ag.size), st.bar, it.pkg.state === 'completed')}</td>
            <td class:muted={!ag.speed}>{formatSpeed(ag.speed)}</td>
            <td><StateLabel status={st} /></td>
            <td class="muted"><EgressLabel id={ag.egressId} /></td>
          </tr>
          {#if open}
            {#each it.children as c (c.id)}
              {@render fileRow(c, true)}
            {/each}
          {/if}
        {:else}
          {@render fileRow(it.dl, false)}
        {/if}
      {:else}
        <tr>
          <td colspan="7" class="empty">
            {#if app.items.length === 0}
              <p>No hay descargas. Pega enlaces con «Añadir enlaces», arrástralos aquí o pulsa <kbd>Ctrl</kbd> <kbd>K</kbd>.</p>
            {:else if app.ui.search.trim()}
              <p>Nada coincide con «{app.ui.search.trim()}».</p>
            {:else}
              <p>No hay descargas en «{bucketLabel}».</p>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#snippet progress(name: string, pct: number, tone: import('../../lib/store/status').Tone, full: boolean)}
  <div class="prog">
    <ProgressBar value={pct} {tone} {full} label="Progreso de {name}" />
    <span class="pct muted tabular">{pct}%</span>
  </div>
{/snippet}

{#snippet fileRow(d: Download, child: boolean)}
  {@const st = statusOf(d, app.now)}
  {@const key = dkey(d.id)}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <tr data-row-key={key} class:child class:sel={app.isSelected(key)} class:done={d.state === 'completed'} onclick={() => rowClick(key)}>
    <td class="nm">
      <div class="name">
        <span class:text-2={child}>{child ? d.rel_path || d.name : d.name}</span>
        {#if st.notResumable}<NotResumable />{/if}
        {#if app.isSelected(key)}<span class="sr-only">(seleccionada)</span>{/if}
      </div>
    </td>
    <td class="muted">{child ? '' : hosterName(d.hoster)}</td>
    <td class="muted">{formatBytes(d.size)}</td>
    <td>
      {#if d.state === 'failed'}
        <RetryButton rowKey={key} name={d.name} />
      {:else}
        {@render progress(d.name, percent(d.done, d.size), st.bar, d.state === 'completed')}
      {/if}
    </td>
    <td class:muted={!d.speed || d.state !== 'downloading'} class:text-2={child && d.state === 'downloading'}>{d.state === 'downloading' ? formatSpeed(d.speed) : '—'}</td>
    <td class:text-2={child}>
      {#if child && d.state === 'downloading' && d.connections > 0}
        {plural(d.connections, 'segmento', 'segmentos')}
      {:else}
        <StateLabel status={st} />
      {/if}
    </td>
    <td class="muted">{#if !child}<EgressLabel id={d.egress_id} fallback="—" />{/if}</td>
  </tr>
  {#if st.fileQuota && (d.state === 'failed' || d.state === 'waiting')}
    <tr class="note" class:child>
      <td colspan="7"><DriveQuotaHelp compact /></td>
    </tr>
  {/if}
{/snippet}

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0 auto 0 0; font-size: 18px; font-weight: 600; letter-spacing: -0.01em; }
  .search {
    display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 0 12px; flex: 0 1 260px;
    border: 1px solid var(--border-strong); border-radius: var(--radius-md); background: var(--surface); color: var(--muted);
  }
  .search input { border: 0; outline: 0; background: transparent; width: 100%; min-height: 34px; color: var(--text); }
  .search:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .add-btn { min-height: 36px; }
  .addp { padding: 16px; }

  .bar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 16px; }
  .pills { display: flex; flex-wrap: wrap; gap: 6px; }
  .pill {
    min-height: 36px; padding: 0 12px; border: 1px solid transparent; border-radius: var(--radius-pill);
    background: transparent; color: var(--muted); cursor: pointer;
  }
  .pill:hover { color: var(--text); }
  .pill[aria-pressed='true'] { border-color: var(--border-strong); background: var(--raised); color: var(--text); }

  .dl { min-width: 880px; }
  .dl th { padding: 10px 12px; }
  .dl td { padding: 12px; }
  .w-prog { width: 190px; }
  tbody tr { cursor: default; }
  tbody tr.sel, tbody tr.sel + tr.note { background: var(--row-hi, var(--raised)); }
  tbody tr.sel td:first-child { box-shadow: inset 2px 0 0 var(--accent); }
  tr.child td { padding-top: 10px; padding-bottom: 10px; }
  tr.child td.nm { padding-left: 40px; }
  tr.done td.nm { color: var(--muted); }
  .name { display: flex; align-items: center; gap: 8px; min-width: 0; overflow-wrap: anywhere; }
  .nowrap, td:not(.nm) { white-space: nowrap; }
  tr.pkg .name :global(.name) { white-space: nowrap; }
  .prog { display: flex; align-items: center; gap: 10px; }
  .pct { width: 36px; text-align: right; }
  .note td { padding-top: 0; color: var(--text-2); white-space: normal; }
  .note.child td { padding-left: 40px; }
  .empty { padding: 32px 16px; text-align: center; color: var(--muted); }
  .empty p { margin: 0; }
  /* Narrow screens: keep name, progress and state; the rest is in the row's
     selection bar and the state text. */
  @media (max-width: 700px) {
    .dl { min-width: 0; }
    .dl th:nth-child(2), .dl td:nth-child(2), .dl th:nth-child(3), .dl td:nth-child(3),
    .dl th:nth-child(5), .dl td:nth-child(5), .dl th:nth-child(7), .dl td:nth-child(7) { display: none; }
    .note td { display: table-cell !important; }
    .w-prog { width: 30%; }
    .dl td { padding: 10px 8px; white-space: normal; }
    .dl .nowrap { display: none; }
    .dl :global(.state) { white-space: normal; }
    .name { flex-wrap: wrap; overflow-wrap: break-word; }
    .pct { display: none; }
    tr.child td.nm { padding-left: 24px; }
    .search { flex: 1 1 100%; }
  }
  kbd { font-family: var(--font-mono); font-size: 11px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 0 4px; }
</style>
