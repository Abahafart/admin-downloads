<script lang="ts">
  // C · Medianoche Descargas (docs/design/Medianoche.dc.html): dashed drop
  // field ("Portapapeles vigilado"), "En curso" + counts, a responsive grid
  // of cards (round 44 px pause/resume, 6 px bar, "x de y GB · speed" + ETA),
  // the quota card with the big countdown and "Rotar ya", failed cards that
  // explain the error in one sentence, and the completed ones summarized at
  // the bottom with "Ver historial".
  import ClipToggle from './ClipToggle.svelte';
  import Icon from '../../components/Icon.svelte';
  import RowActions from '../common/RowActions.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { runLine } from '../../lib/commands';
  import { formatBytes, formatSpeed, formatETA, percent } from '../../lib/store/format';
  import { hosterName } from '../../lib/store/labels';
  import type { Download } from '../../lib/api/types';
  import {
    doneOf, downloadVM, egressPhrase, failSentence, filesPhrase, itemVM, ongoingSummary, waitSentence, type RowVM,
  } from '../common/model';

  let link = $state('');
  let busy = $state(false);
  let over = $state(false);
  let history = $state(false);

  const rows = $derived(app.visible.map((it) => itemVM(it, app.now)));
  const ongoing = $derived(rows.filter((v) => v.st.state !== 'completed'));
  const done = $derived(rows.filter((v) => v.st.state === 'completed'));
  const hasDrive = $derived(app.accounts.some((a) => a.hoster === 'gdrive' && a.enabled && a.status === 'ok'));
  const doneSize = $derived(done.reduce((n, v) => n + Math.max(0, v.size), 0));
  const q = $derived(app.ui.search.trim());
  const ACTIVE = new Set(['downloading', 'verifying', 'postprocessing', 'resolving']);

  async function add(e: Event) {
    e.preventDefault();
    const v = link.trim();
    if (!v || busy) return;
    busy = true;
    const ok = await runLine(app, v);
    busy = false;
    if (ok) link = '';
  }

  async function drop(e: DragEvent) {
    e.preventDefault();
    over = false;
    const dt = e.dataTransfer;
    let t = dt?.getData('text/uri-list') || dt?.getData('text/plain') || '';
    if (!t && dt?.files.length) t = (await Promise.all([...dt.files].map((f) => f.text()))).join('\n');
    if (t) await app.addLinks(t);
  }

  function select(key: string, e: MouseEvent) {
    if ((e.target as HTMLElement).closest('button, a, input')) return;
    e.stopPropagation();
    app.select(app.ui.selected === key ? null : key);
  }

  function meta(vm: RowVM): string {
    const h = hosterName(vm.hoster);
    if (vm.kind === 'package') return `${h} · carpeta · ${filesPhrase(vm.filesTotal)}`;
    if (vm.st.state === 'failed' || vm.st.state === 'completed') return h;
    if (vm.st.state === 'queued') return `${h} · ${formatBytes(vm.size)}`;
    const eg = egressPhrase(vm.egressId, app.data.egress);
    return eg ? `${h} · ${eg}` : h;
  }

  /** Selected row inside this card (the card itself or one of its files). */
  function selectedIn(vm: RowVM): RowVM | undefined {
    const s = app.ui.selected;
    if (!s) return undefined;
    if (s === vm.key) return vm;
    const c = vm.children.find((d) => `d:${d.id}` === s);
    return c ? downloadVM(c, app.now) : undefined;
  }

  function rotateFor(vm: RowVM) {
    void app.rotate(vm.egressId && vm.egressId !== 'direct' ? vm.egressId : '');
  }

  function childState(d: Download): string {
    const st = app.statusOf(d);
    if (d.state === 'downloading') return `${percent(d.done, d.size)}% · ${formatSpeed(d.speed)}`;
    return st.label;
  }
</script>

<div class="drop" class:over role="group" aria-label="Añadir enlaces" ondragover={(e) => { e.preventDefault(); over = true; }} ondragleave={() => (over = false)} ondrop={drop}>
  <form class="dform" onsubmit={add}>
    <label class="dl">
      <Icon name="plus" size={18} stroke={1.8} />
      <span class="sr-only">Añadir enlaces (o escribir un comando como /pausa)</span>
      <input type="text" bind:value={link} placeholder="Pega o arrastra enlaces de MEGA, MediaFire, Google Drive, TeraBox…" autocomplete="off" spellcheck="false" />
    </label>
    {#if link.trim()}<button type="submit" class="btn btn-primary addb" disabled={busy}>{busy ? 'Añadiendo…' : 'Añadir'}</button>{/if}
  </form>
  <ClipToggle />
</div>

<div class="head">
  <h1>En curso</h1>
  <div class="hr">
    <span class="muted tabular">{ongoingSummary(app.counts)}</span>
    <label class="search">
      <Icon name="search" size={14} />
      <span class="sr-only">Buscar</span>
      <input type="search" placeholder="Buscar" bind:value={app.ui.search} data-shortcut="search" aria-keyshortcuts="/" />
    </label>
  </div>
</div>

{#if ongoing.length}
  <div class="cards">
    {#each ongoing as vm (vm.key)}
      {@render card(vm)}
    {/each}
  </div>
{:else}
  <p class="empty">
    {#if app.items.length === 0}
      No hay descargas todavía. Pega enlaces arriba o arrástralos a la ventana.
    {:else if q}
      Nada en curso coincide con «{q}».
    {:else}
      Nada en curso ahora mismo.
    {/if}
  </p>
{/if}

{#if done.length}
  <div class="donebar">
    <span>
      Completadas ·
      {#if done.length === 1}
        {done[0].name}{done[0].kind === 'package' ? '/' : ''} · {formatBytes(done[0].size)}
      {:else}
        {done.length} · {formatBytes(doneSize)} en total
      {/if}
    </span>
    <button type="button" class="linkb" aria-expanded={history} aria-controls="m-hist" onclick={() => (history = !history)}>{history ? 'Ocultar historial' : 'Ver historial'}</button>
  </div>
  {#if history}
    <ul id="m-hist" class="hist" aria-label="Completadas">
      {#each done as vm (vm.key)}
        {@const sel = selectedIn(vm)}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
        <li data-row-key={vm.key} class:sel={!!sel} onclick={(e) => select(vm.key, e)}>
          <span class="hn">{vm.name}{vm.kind === 'package' ? '/' : ''}</span>
          <span class="muted">{hosterName(vm.hoster)} · {formatBytes(vm.size)}</span>
          {#if sel}<span class="sr-only">(seleccionada)</span><div class="ra-wrap"><RowActions vm={sel} /></div>{/if}
        </li>
      {/each}
    </ul>
  {/if}
{/if}

{#snippet card(vm: RowVM)}
  {@const st = vm.st}
  {@const sel = selectedIn(vm)}
  {@const open = vm.kind === 'package' && app.isExpanded(vm.packageId)}
  {@const quiet = st.state === 'paused' || st.state === 'failed' || st.state === 'queued'}
  {@const amount = doneOf(vm.done, vm.size)}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <article class="card" class:quiet class:sel={sel?.key === vm.key} data-row-key={vm.key} aria-labelledby="m-t-{vm.key}" onclick={(e) => select(vm.key, e)}>
    <div class="ct">
      <div class="tt">
        <span class="meta">{meta(vm)}</span>
        <h2 id="m-t-{vm.key}">{vm.name}{#if sel?.key === vm.key}<span class="sr-only">{' '}(seleccionada)</span>{/if}</h2>
      </div>
      {#if st.state === 'waiting'}
        <span class="count tabular" aria-label="Reintenta en {st.detail}">{st.detail || '—'}</span>
      {:else if st.state === 'paused'}
        <button type="button" class="round play" aria-label="Reanudar {vm.name}" onclick={() => app.resume(vm.key)}><svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false"><path d="M8 5l11 7-11 7z" /></svg></button>
      {:else if st.state === 'failed'}
        <button type="button" class="pillb" aria-label="Reintentar {vm.name}" onclick={() => app.retry(vm.key)}>Reintentar</button>
      {:else if st.state === 'queued'}
        {#if st.notResumable}<span class="chip">no reanudable</span>{/if}
      {:else if st.canPause}
        <button type="button" class="round" aria-label="Pausar {vm.name}" onclick={() => app.pause(vm.key)}><svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg></button>
      {/if}
    </div>

    {#if ACTIVE.has(st.state)}
      {@render bar(vm, 'accent')}
      <div class="line tabular">
        <span><span class="tx">{amount.done}</span> de {amount.total}{vm.speed ? ` · ${formatSpeed(vm.speed)}` : st.state !== 'downloading' ? ` · ${st.short}` : ''}{#if vm.pkg?.files_failed}<span class="tone-err">{' '}· {vm.pkg.files_failed} con error</span>{/if}</span>
        <span>{formatETA(vm.eta)}</span>
      </div>
    {:else if st.state === 'waiting'}
      {@render bar(vm, 'warn')}
      <div class="line">
        <span>{waitSentence(vm, app.data.egress, app.data.rotating)}</span>
        {#if !st.fileQuota}<button type="button" class="linkb tx" aria-keyshortcuts="i" onclick={() => rotateFor(vm)}>Rotar ya</button>{/if}
      </div>
    {:else if st.state === 'paused'}
      {@render bar(vm, 'paused')}
      <div class="line tabular"><span>Pausada · {amount.done} de {amount.total}</span><span>{vm.pct}%</span></div>
    {:else if st.state === 'failed'}
      <p class="say err">
        {failSentence(vm, hasDrive)}
        {#if st.fileQuota && !hasDrive}<a href="#/cuentas?alta=gdrive">Conectar cuenta de Drive</a>{/if}
      </p>
    {:else if st.state === 'queued'}
      <p class="say">{st.notResumable ? 'En cola. El servidor no admite Range: si se corta, empieza de cero.' : 'En cola. Empieza en cuanto haya un hueco.'}</p>
    {/if}

    {#if vm.kind === 'package' && vm.children.length}
      <button type="button" class="linkb files" aria-expanded={open} aria-controls="m-f-{vm.key}" onclick={() => app.toggleExpanded(vm.packageId)}>
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12} stroke={2.4} />{open ? 'Ocultar archivos' : `Ver ${filesPhrase(vm.children.length)}`}
      </button>
      {#if open}
        <ul id="m-f-{vm.key}" class="flist" aria-label="Archivos de {vm.name}">
          {#each vm.children as c (c.id)}
            {@const ck = `d:${c.id}`}
            <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
            <li data-row-key={ck} class:sel={app.isSelected(ck)} class:fin={c.state === 'completed'} onclick={(e) => select(ck, e)}>
              <span class="fn">{c.rel_path || c.name}{#if app.isSelected(ck)}<span class="sr-only">{' '}(seleccionada)</span>{/if}</span>
              <span class="fs tabular" class:tone-warn={c.state === 'waiting'} class:tone-err={c.state === 'failed'}>{childState(c)}</span>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}

    {#if sel}<RowActions vm={sel} />{/if}
  </article>
{/snippet}

{#snippet bar(vm: RowVM, tone: 'accent' | 'warn' | 'paused')}
  <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={vm.pct} aria-label="Progreso de {vm.name}">
    <div class="fill {tone}" style:width="{vm.pct}%"></div>
  </div>
{/snippet}

<style>
  .drop {
    display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; min-height: 56px; padding: 0 20px;
    border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); color: var(--muted);
  }
  .drop.over { border-color: var(--accent); }
  .drop:focus-within { border-color: var(--accent); }
  .dform { flex: 1 1 280px; display: flex; align-items: center; gap: 10px; min-width: 0; }
  .dl { flex: 1; display: flex; align-items: center; gap: 14px; min-width: 0; cursor: text; }
  .dl input { flex: 1; min-width: 0; min-height: 52px; border: 0; outline: 0; background: transparent; color: var(--text); padding: 0; }
  .dl input::placeholder { color: var(--muted); opacity: 1; }
  .addb { border-radius: var(--radius-pill); min-height: 44px; }

  .head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
  h1 { margin: 0; font-size: 22px; font-weight: 600; letter-spacing: -0.02em; }
  .hr { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; }
  .search {
    display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 0 14px; width: 200px; max-width: 100%;
    border: 1px solid var(--border); border-radius: var(--radius-pill); background: var(--surface); color: var(--muted);
  }
  .search input { flex: 1; min-width: 0; min-height: 34px; border: 0; outline: 0; background: transparent; color: var(--text); }
  .search input::placeholder { color: var(--muted); opacity: 1; }
  .search:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }

  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(340px, 100%), 1fr)); gap: 16px; }
  .card {
    display: flex; flex-direction: column; gap: 14px; padding: 20px; min-width: 0;
    border-radius: var(--radius-lg); background: var(--surface); border: 1px solid var(--border);
  }
  .card.quiet { background: transparent; }
  .card.sel { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
  .ct { display: flex; align-items: flex-start; gap: 12px; }
  .tt { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .meta { color: var(--muted); font-size: 12px; }
  h2 { margin: 0; font-size: 16px; font-weight: 500; overflow-wrap: anywhere; }
  .round {
    flex: none; width: 44px; height: 44px; display: flex; align-items: center; justify-content: center;
    border: 1px solid var(--border-strong); border-radius: var(--radius-pill); background: transparent; color: var(--text); cursor: pointer;
  }
  .round:hover { background: var(--raised); }
  .round.play { border: 0; background: var(--accent); color: var(--on-accent); }
  .pillb {
    flex: none; min-height: 44px; padding: 0 16px; border: 1px solid var(--border-strong); border-radius: var(--radius-pill);
    background: transparent; color: var(--text); cursor: pointer;
  }
  .pillb:hover { background: var(--raised); }
  .count { font-size: 20px; font-weight: 600; color: var(--warn); }
  .chip {
    flex: none; padding: 2px 8px; border-radius: var(--radius-sm); border: 1px solid var(--border-strong); color: var(--muted); font-size: 12px;
  }
  .bar { height: 6px; border-radius: var(--radius-pill); background: var(--track); overflow: hidden; }
  .fill { height: 100%; border-radius: inherit; background: var(--accent); transition: width 0.4s linear; }
  .fill.warn { background: var(--warn); }
  .fill.paused { background: var(--paused); }
  .line { display: flex; justify-content: space-between; align-items: center; gap: 12px; color: var(--muted); font-size: 13px; }
  .tx { color: var(--text); }
  .say { margin: 0; color: var(--muted); font-size: 13px; }
  .say.err { color: var(--err); }
  .say a { color: var(--text); margin-left: 4px; }
  .linkb {
    min-height: 36px; margin: -8px 0; padding: 0; border: 0; background: transparent; color: var(--muted);
    text-decoration: underline; text-underline-offset: 3px; cursor: pointer; white-space: nowrap;
  }
  .linkb.tx { color: var(--text); }
  .linkb:hover { color: var(--text); }
  .files { align-self: flex-start; display: inline-flex; align-items: center; gap: 6px; text-decoration: none; font-size: 13px; }
  .flist { list-style: none; margin: -6px 0 0; padding: 0; display: flex; flex-direction: column; border-top: 1px solid var(--border); font-size: 13px; }
  .flist li {
    display: flex; flex-wrap: wrap; justify-content: space-between; gap: 2px 12px; padding: 8px 6px; border-bottom: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .flist li.sel { background: var(--raised); }
  .flist li.fin .fn { color: var(--muted); }
  .fn { min-width: 0; overflow-wrap: anywhere; color: var(--text-2); }
  .fs { color: var(--muted); }
  .card :global(.ra .btn) { border-radius: var(--radius-pill); }

  .empty { margin: 0; padding: 32px 20px; border: 1px solid var(--border); border-radius: var(--radius-lg); color: var(--muted); text-align: center; }
  .donebar {
    display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap;
    padding-top: 16px; border-top: 1px solid var(--border); color: var(--muted);
  }
  .hist { list-style: none; margin: -12px 0 0; padding: 0; display: flex; flex-direction: column; }
  .hist li { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 4px 12px; padding: 10px 8px; border-bottom: 1px solid var(--border); border-radius: var(--radius-sm); }
  .hist li.sel { background: var(--surface); }
  .hn { overflow-wrap: anywhere; min-width: 0; }
  .ra-wrap { flex-basis: 100%; }
  .ra-wrap :global(.btn) { border-radius: var(--radius-pill); }
  @media (max-width: 560px) {
    .drop { padding: 6px 14px; }
    .search { width: 100%; }
    .hr { width: 100%; }
    .card { padding: 16px; }
  }
</style>
