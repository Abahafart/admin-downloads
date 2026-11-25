<script lang="ts">
  // E · Enfoque Descargas (docs/design/Enfoque.dc.html): the active download
  // in large type (percent, speed, restante, descargado, salida) with calm
  // round buttons, a single link field and the compact "Después" list.
  import Icon from '../../components/Icon.svelte';
  import RowActions from '../common/RowActions.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { runLine } from '../../lib/commands';
  import { formatBytes, formatSpeed, percent } from '../../lib/store/format';
  import { hosterName } from '../../lib/store/labels';
  import { doneOf, downloadVM, failSentence, filesPhrase, focusItem, humanDuration, itemVM, type RowVM } from '../common/model';

  let link = $state('');
  let busy = $state(false);
  const rows = $derived(app.visible.map((it) => itemVM(it, app.now)));
  const focus = $derived(focusItem(rows));
  const rest = $derived(rows.filter((v) => v.key !== focus?.key));
  const hasDrive = $derived(app.accounts.some((a) => a.hoster === 'gdrive' && a.enabled && a.status === 'ok'));
  const q = $derived(app.ui.search.trim());

  async function add(e: Event) {
    e.preventDefault();
    const v = link.trim();
    if (!v || busy) return;
    busy = true;
    const ok = await runLine(app, v);
    busy = false;
    if (ok) link = '';
  }

  function click(key: string, e: MouseEvent) {
    if ((e.target as HTMLElement).closest('button, a, input')) return;
    app.select(app.ui.selected === key ? null : key);
  }

  function focusMeta(vm: RowVM): string {
    const what = vm.st.state === 'downloading' ? 'Descargando ahora' : vm.st.state === 'waiting' ? `En espera · ${vm.st.short.toLowerCase()}` : vm.st.short;
    const kind = vm.kind === 'package' ? `carpeta de ${filesPhrase(vm.filesTotal)}` : '';
    return [what, hosterName(vm.hoster), kind].filter(Boolean).join(' · ');
  }

  function right(vm: RowVM): string {
    const st = vm.st;
    switch (st.state) {
      case 'downloading':
        return formatSpeed(vm.speed);
      case 'waiting': {
        const what = st.quotaScope === 'ip' ? 'Cuota IP' : st.quotaScope === 'account' ? 'Cuota cuenta' : st.quotaScope === 'file' ? 'Cuota archivo' : 'En espera';
        return st.detail ? `${what} · ${st.detail}` : what;
      }
      default:
        return st.short;
    }
  }

  function selectedIn(vm: RowVM): RowVM | undefined {
    const s = app.ui.selected;
    if (!s) return undefined;
    if (s === vm.key) return vm;
    const c = vm.children.find((d) => `d:${d.id}` === s);
    return c ? downloadVM(c, app.now) : undefined;
  }
</script>

{#if focus}
  {@const st = focus.st}
  {@const amount = doneOf(focus.done, focus.size)}
  {@const open = focus.kind === 'package' && app.isExpanded(focus.packageId)}
  {@const fsel = selectedIn(focus)}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <section class="now" class:sel={fsel?.key === focus.key} aria-labelledby="e-now" data-row-key={focus.key} onclick={(e) => click(focus.key, e)}>
    <div class="nh">
      <div class="tt">
        <span class="meta">{focusMeta(focus)}</span>
        <h1 id="e-now">{focus.name}{#if fsel?.key === focus.key}<span class="sr-only">{' '}(seleccionada)</span>{/if}</h1>
      </div>
      {#if st.state === 'waiting'}
        <span class="big warn tabular" aria-label="Reintenta en {st.detail}">{st.detail || '—'}</span>
      {:else}
        <span class="big tabular">{focus.pct}%</span>
      {/if}
    </div>
    <div class="bar8" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={focus.pct} aria-label="Progreso de {focus.name}">
      <div class="fill" class:warn={st.state === 'waiting'} style:width="{focus.pct}%"></div>
    </div>
    <dl class="stats tabular">
      <div><dt>Velocidad</dt><dd>{formatSpeed(focus.speed)}</dd></div>
      <div><dt>Restante</dt><dd>{st.state === 'waiting' ? `reintenta en ${st.detail}` : humanDuration(focus.eta)}</dd></div>
      <div><dt>Descargado</dt><dd>{amount.done} de {amount.total}</dd></div>
      <div><dt>Salida</dt><dd>{app.egressLabel(focus.egressId) || 'Directa'}</dd></div>
    </dl>
    {#if st.fileQuota}<p class="say">Google Drive limita este archivo, no tu IP: rotar no ayuda. {#if !hasDrive}<a href="#/cuentas?alta=gdrive">Conectar cuenta de Drive</a>{/if}</p>{/if}
    <div class="acts">
      {#if st.canResume}
        <button type="button" class="b-main" aria-keyshortcuts="p" onclick={() => app.resume(focus.key)}>Reanudar</button>
      {:else if st.canPause}
        <button type="button" class="b-main" aria-keyshortcuts="p" onclick={() => app.pause(focus.key)}>Pausar</button>
      {/if}
      {#if !st.fileQuota}
        <button type="button" class="b-line" aria-keyshortcuts="i" onclick={() => app.rotate(focus.egressId && focus.egressId !== 'direct' ? focus.egressId : '')}>Rotar IP</button>
      {/if}
      {#if focus.kind === 'package' && focus.children.length}
        <button type="button" class="b-ghost" aria-expanded={open} aria-controls="e-files" onclick={() => app.toggleExpanded(focus.packageId)}>{open ? 'Ocultar archivos' : 'Ver archivos'}</button>
      {/if}
      <button type="button" class="b-ghost" onclick={() => app.askDelete(focus.key)} aria-label="Eliminar {focus.name}" aria-keyshortcuts="Delete">Eliminar</button>
    </div>
    {#if open}
      <ul id="e-files" class="files" aria-label="Archivos de {focus.name}">
        {#each focus.children as c (c.id)}
          {@const cvm = downloadVM(c, app.now)}
          {@render row(cvm, true)}
        {/each}
      </ul>
    {/if}
  </section>
{:else}
  <section class="now idle" aria-labelledby="e-now">
    <span class="meta">{app.items.length ? 'Nada descargándose ahora' : 'Sin descargas'}</span>
    <h1 id="e-now">{app.items.length ? 'Todo en calma' : 'Empieza pegando un enlace'}</h1>
  </section>
{/if}

<form class="drop" onsubmit={add}>
  <label class="dl">
    <Icon name="link" size={16} />
    <span class="sr-only">Añadir enlaces (o escribir un comando como /pausa)</span>
    <input type="text" bind:value={link} placeholder="Pega un enlace y pulsa Intro" autocomplete="off" spellcheck="false" />
  </label>
  {#if link.trim()}<button type="submit" class="b-main sm" disabled={busy}>{busy ? 'Añadiendo…' : 'Añadir'}</button>{/if}
</form>

<section class="next" aria-labelledby="e-next">
  <div class="nxh">
    <h2 id="e-next">Después · {rest.length}</h2>
    <label class="search">
      <span class="sr-only">Buscar</span>
      <input type="search" placeholder="Buscar" bind:value={app.ui.search} data-shortcut="search" aria-keyshortcuts="/" />
    </label>
  </div>
  <ul class="list">
    {#each rest as vm (vm.key)}
      {@render row(vm, false)}
      {#if vm.kind === 'package' && app.isExpanded(vm.packageId)}
        {#each vm.children as c (c.id)}
          {@render row(downloadVM(c, app.now), true)}
        {/each}
      {/if}
    {:else}
      <li class="empty">{q ? `Nada coincide con «${q}».` : 'No hay nada más en la cola.'}</li>
    {/each}
  </ul>
</section>

{#snippet row(vm: RowVM, child: boolean)}
  {@const st = vm.st}
  {@const sel = app.isSelected(vm.key)}
  {@const quiet = st.state === 'paused' || st.state === 'queued' || st.state === 'completed'}
  {@const bar = st.state === 'downloading' || st.state === 'waiting' || st.state === 'paused' || st.state === 'verifying' || st.state === 'postprocessing'}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <li class="row" class:child class:quiet class:done={st.state === 'completed'} class:sel data-row-key={vm.key} onclick={(e) => click(vm.key, e)}>
    <span class="nm">
      {#if vm.kind === 'package'}
        <button type="button" class="tog" aria-expanded={app.isExpanded(vm.packageId)} onclick={() => app.toggleExpanded(vm.packageId)}>
          <span class="n1">{vm.name}/</span><span class="sr-only">{app.isExpanded(vm.packageId) ? '(plegar carpeta)' : '(desplegar carpeta)'}</span>
        </button>
        <span class="mu">{' '}· {hosterName(vm.hoster)} · {filesPhrase(vm.filesTotal)}</span>
      {:else}
        <span class="n1">{child ? vm.dl?.rel_path || vm.name : vm.name}</span>{#if !child}<span class="mu">{' '}· {hosterName(vm.hoster)}</span>{/if}
      {/if}
      {#if st.notResumable && st.state !== 'completed'}<span class="mu">{' '}· no reanudable</span>{/if}
      {#if sel}<span class="sr-only">{' '}(seleccionada)</span>{/if}
    </span>
    {#if st.state === 'failed'}
      <span class="err" title={st.fileQuota ? undefined : failSentence(vm, hasDrive)}>{st.fileQuota ? 'Cuota del archivo' : st.detail ? st.detail.charAt(0).toUpperCase() + st.detail.slice(1) : 'Fallida'}</span>
      <button type="button" class="b-line sm w140" aria-label="Reintentar {vm.name}" onclick={() => app.retry(vm.key)}>Reintentar</button>
    {:else}
      {#if bar}
        <span class="bar4" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={vm.pct} aria-label="Progreso de {vm.name}">
          <span class="fill" class:warn={st.state === 'waiting'} class:paused={st.state === 'paused'} style:width="{vm.pct}%"></span>
        </span>
      {/if}
      <span class="rt tabular" class:warn={st.state === 'waiting'} class:tx={st.state === 'downloading'}>{right(vm)}{child && st.state === 'downloading' ? ` · ${percent(vm.done, vm.size)}%` : ''}</span>
    {/if}
    {#if st.state === 'failed' && st.fileQuota}
      <span class="why">{failSentence(vm, hasDrive)}{#if st.fileQuota && !hasDrive} <a href="#/cuentas?alta=gdrive">Conectar cuenta de Drive</a>{/if}</span>
    {:else if st.state === 'completed' && !child}
      <span class="sr-only">{formatBytes(vm.size)}</span>
    {/if}
  </li>
  {#if sel && !(focus && vm.key === focus.key)}
    <li class="ra-row"><RowActions {vm} /></li>
  {/if}
{/snippet}

<style>
  .now { display: flex; flex-direction: column; gap: 20px; border-radius: var(--radius-lg); }
  .now.sel { outline: 1px solid var(--border-strong); outline-offset: 12px; }
  .nh { display: flex; align-items: baseline; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .tt { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .meta { color: var(--muted); font-size: 13px; }
  h1 { margin: 0; font-size: 28px; font-weight: 600; letter-spacing: -0.02em; overflow-wrap: anywhere; }
  .big { font-size: 44px; font-weight: 500; letter-spacing: -0.03em; color: var(--accent); }
  .big.warn { color: var(--warn); }
  .bar8 { height: 8px; border-radius: var(--radius-pill); background: var(--track); overflow: hidden; }
  .bar8 .fill { height: 100%; border-radius: inherit; background: var(--accent); transition: width 0.4s linear; }
  .bar8 .fill.warn { background: var(--warn); }
  .stats { margin: 0; display: grid; grid-template-columns: repeat(auto-fit, minmax(min(140px, 100%), 1fr)); gap: 16px; }
  .stats div { display: flex; flex-direction: column; gap: 2px; }
  .stats dt { color: var(--muted); font-size: 12px; }
  .stats dd { margin: 0; font-size: 16px; font-weight: 500; }
  .say { margin: 0; color: var(--text-2); }
  .say a, .why a { color: var(--text); }
  .acts { display: flex; flex-wrap: wrap; gap: 10px; }
  .b-main, .b-line, .b-ghost {
    min-height: 44px; padding: 0 20px; border-radius: var(--radius-pill); font: inherit; cursor: pointer;
  }
  .b-main { border: 0; background: var(--text); color: var(--bg); font-weight: 600; }
  .b-main:hover { text-decoration: underline; }
  .b-line { border: 1px solid var(--border-strong); background: transparent; color: var(--text); }
  .b-line:hover { background: var(--surface); }
  .b-ghost { border: 1px solid transparent; background: transparent; color: var(--muted); }
  .b-ghost:hover { color: var(--text); }
  .sm { min-height: 36px; padding: 0 16px; }
  .w140 { width: 140px; flex: none; }
  .idle { gap: 4px; }

  .drop {
    display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 0 8px 0 20px;
    border-radius: var(--radius-pill); background: var(--surface); color: var(--muted);
  }
  .drop:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .dl { flex: 1; display: flex; align-items: center; gap: 12px; min-width: 0; cursor: text; }
  .dl input { flex: 1; min-width: 0; min-height: 48px; border: 0; outline: 0; background: transparent; color: var(--text); padding: 0; }
  .dl input::placeholder { color: var(--muted); opacity: 1; }

  .next { display: flex; flex-direction: column; }
  .nxh { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 8px; }
  h2 { margin: 0; font-size: 13px; font-weight: 600; color: var(--muted); }
  .search input {
    width: 150px; min-height: 36px; padding: 0 14px; border: 0; border-radius: var(--radius-pill); background: transparent; color: var(--text);
  }
  .search input:focus { background: var(--surface); outline: 0; }
  .search input::placeholder { color: var(--muted); opacity: 1; }
  .search:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: var(--radius-pill); }
  .list, .files { list-style: none; margin: 0; padding: 0; }
  .files { border-top: 1px solid var(--border); }
  .row {
    display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; min-height: 56px; padding: 8px 0;
    border-bottom: 1px solid var(--border);
  }
  .list > .row:last-of-type { border-bottom: 0; }
  .row.sel { background: var(--surface); margin: 0 -12px; padding: 8px 12px; border-radius: var(--radius-md); }
  .row.child { min-height: 44px; padding-left: 20px; font-size: 13px; }
  .row.child.sel { padding-left: 32px; }
  .nm { flex: 1 1 200px; min-width: 0; overflow-wrap: anywhere; }
  .mu { color: var(--muted); }
  .quiet { color: var(--muted); }
  .quiet .n1 { color: var(--text); }
  .done .n1 { color: var(--muted); }
  .tog { min-height: 36px; margin: -8px 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; cursor: pointer; text-align: left; }
  .tog:hover .n1 { text-decoration: underline; text-underline-offset: 4px; }
  .bar4 { width: 120px; height: 4px; border-radius: var(--radius-pill); background: var(--track); overflow: hidden; flex: none; }
  .bar4 .fill { display: block; height: 100%; border-radius: inherit; background: var(--accent); }
  .bar4 .fill.warn { background: var(--warn); }
  .bar4 .fill.paused { background: var(--paused); }
  .rt { width: 140px; text-align: right; flex: none; }
  .rt.warn { color: var(--warn); }
  .rt.tx { color: var(--text); }
  .err { color: var(--err); }
  .why { flex-basis: 100%; color: var(--muted); font-size: 13px; padding-bottom: 4px; }
  .ra-row { padding: 6px 0 14px; border-bottom: 1px solid var(--border); }
  .ra-row :global(.btn) { border-radius: var(--radius-pill); }
  .empty { padding: 20px 0; color: var(--muted); }
  @media (max-width: 560px) {
    .nm { flex-basis: 100%; }
    .rt { width: auto; margin-left: auto; }
    .bar4 { width: 96px; }
    .big { font-size: 36px; }
    .search input { width: 120px; }
    .row.sel { margin: 0 -8px; padding: 8px; }
  }
</style>
