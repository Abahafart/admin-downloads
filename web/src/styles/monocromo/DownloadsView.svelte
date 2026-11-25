<script lang="ts">
  // D · Monocromo Descargas (docs/design/Monocromo.dc.html): "Cola · N",
  // thin-line list. No state colors: states differ only by text, italics
  // (quota wait), weight (failed), muted rows (paused/queued/completed) and
  // strike-through (completed). The accent only fills active bars and the
  // primary button.
  import AddLinks from '../../components/AddLinks.svelte';
  import RowActions from '../common/RowActions.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { formatBytes, formatETA, formatSpeed } from '../../lib/store/format';
  import { hosterName } from '../../lib/store/labels';
  import { downloadVM, failSentence, filesPhrase, itemVM, type RowVM } from '../common/model';

  let adding = $state(false);
  const rows = $derived(app.visible.map((it) => itemVM(it, app.now)));
  const hasDrive = $derived(app.accounts.some((a) => a.hoster === 'gdrive' && a.enabled && a.status === 'ok'));
  const q = $derived(app.ui.search.trim());
  const ACTIVE = new Set(['downloading', 'verifying', 'postprocessing', 'resolving']);

  function click(key: string, e: MouseEvent) {
    if ((e.target as HTMLElement).closest('button, a, input')) return;
    app.select(app.ui.selected === key ? null : key);
  }

  function right(vm: RowVM): string {
    const st = vm.st;
    if (ACTIVE.has(st.state)) return [`${vm.pct}%`, formatETA(vm.eta)].filter(Boolean).join(' · ');
    if (st.state === 'waiting') return st.detail ? `reintenta en ${st.detail}` : '';
    if (st.state === 'paused') return `${vm.pct}%`;
    return formatBytes(vm.size);
  }

  function middle(vm: RowVM): string {
    return vm.st.state === 'downloading' ? formatSpeed(vm.speed) : vm.st.short;
  }

  function note(vm: RowVM): string {
    const st = vm.st;
    if (st.state === 'failed') return st.fileQuota ? 'Cuota del archivo excedida — rotar IP no ayuda' : failSentence(vm, hasDrive);
    if (st.state === 'waiting' && st.fileQuota) return 'Cuota del archivo — rotar IP no ayuda';
    if (st.notResumable && st.state !== 'completed') return 'No reanudable';
    return '';
  }
</script>

<header class="head">
  <h1>Cola · {app.counts.todas}</h1>
  <label class="search">
    <span class="sr-only">Buscar</span>
    <input type="search" placeholder="Buscar" bind:value={app.ui.search} data-shortcut="search" aria-keyshortcuts="/" />
  </label>
  <button type="button" class="b2" onclick={() => app.togglePauseAll()}>{app.data.stats.paused ? 'Reanudar todo' : 'Pausar todo'}</button>
  <button type="button" class="b1" aria-expanded={adding} aria-controls="d-add" onclick={() => (adding = !adding)}>+ Añadir enlaces</button>
</header>

{#if adding}
  <section id="d-add" class="addp" aria-label="Añadir enlaces">
    <AddLinks autofocus onadded={() => (adding = false)} id="d-add-links" />
  </section>
{/if}

<div class="scroll">
  <ol class="list" aria-label="Cola de descargas">
    {#each rows as vm (vm.key)}
      {@render line(vm, false)}
      {#if vm.kind === 'package' && app.isExpanded(vm.packageId)}
        {#each vm.children as c (c.id)}
          {@render line(downloadVM(c, app.now), true)}
        {/each}
      {/if}
    {:else}
      <li class="empty">
        {#if app.items.length === 0}
          Sin descargas. Usa «Añadir enlaces», pega con Ctrl+V o arrastra enlaces a la ventana.
        {:else if q}
          Nada coincide con «{q}».
        {:else}
          Nada con este filtro.
        {/if}
      </li>
    {/each}
  </ol>
</div>

{#snippet line(vm: RowVM, child: boolean)}
  {@const st = vm.st}
  {@const sel = app.isSelected(vm.key)}
  {@const n = note(vm)}
  {@const quiet = st.state === 'paused' || st.state === 'queued' || st.state === 'completed'}
  {@const showBar = ACTIVE.has(st.state) || st.state === 'waiting' || st.state === 'paused'}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <li class="ln" class:child class:quiet class:sel class:mid={st.state === 'failed'} data-row-key={vm.key} onclick={(e) => click(vm.key, e)}>
    <div class="c1">
      <span class="nm" class:strike={st.state === 'completed'}>
        {#if vm.kind === 'package'}
          <button type="button" class="tog" aria-expanded={app.isExpanded(vm.packageId)} onclick={() => app.toggleExpanded(vm.packageId)}>
            {vm.name}/ <span class="sub">{filesPhrase(vm.filesTotal)}</span>
            <span class="sr-only">{app.isExpanded(vm.packageId) ? '(plegar carpeta)' : '(desplegar carpeta)'}</span>
          </button>
        {:else}
          {child ? vm.dl?.rel_path || vm.name : vm.name}
        {/if}
        {#if sel}<span class="sr-only">{' '}(seleccionada)</span>{/if}
      </span>
      {#if showBar}
        <div class="track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={vm.pct} aria-label="Progreso de {vm.name}">
          <div class="fill" class:dash={st.state === 'waiting'} class:still={st.state === 'paused'} style:width="{vm.pct}%"></div>
        </div>
      {/if}
      {#if n}
        <span class="note">
          {n}{#if st.fileQuota && !hasDrive}{' · '}<a href="#/cuentas?alta=gdrive">Conectar cuenta de Drive</a>{/if}
        </span>
      {/if}
    </div>
    <span class="c2">{child ? '' : hosterName(vm.hoster)}</span>
    <span class="c3" class:it={st.state === 'waiting'} class:bold={st.state === 'failed'}>{middle(vm)}</span>
    <span class="c4">
      {#if st.state === 'failed'}
        <button type="button" class="b2 sm" aria-label="Reintentar {vm.name}" onclick={() => app.retry(vm.key)}>Reintentar</button>
      {:else}
        {right(vm)}
      {/if}
    </span>
  </li>
  {#if sel}
    <li class="acts"><RowActions vm={vm} /></li>
  {/if}
{/snippet}

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0 auto 0 0; font-size: 14px; font-weight: 500; text-transform: uppercase; letter-spacing: 0.12em; }
  .search input {
    width: 180px; min-height: 40px; padding: 0 2px; border: 0; border-bottom: 1px solid var(--border-strong); border-radius: 0;
    background: transparent; color: var(--text);
  }
  .search input::placeholder { color: var(--muted); opacity: 1; }
  .search input:focus { outline: 0; border-bottom-color: var(--text); }
  .search:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .b2, .b1 { min-height: 40px; padding: 0 18px; border-radius: 0; font: inherit; cursor: pointer; }
  .b2 { border: 1px solid var(--border-strong); background: transparent; color: var(--text); }
  .b2:hover { border-color: var(--text); }
  .b1 { min-height: 44px; border: 0; background: var(--accent); color: var(--on-accent); font-weight: 500; }
  .b1:hover { text-decoration: underline; }
  .b2.sm { min-height: 36px; padding: 0 14px; }
  .addp { padding: 18px 0; border-top: 1px solid var(--border-strong); }

  .scroll { overflow-x: auto; margin-left: -12px; padding-left: 12px; }
  .list { list-style: none; margin: 0; padding: 0; min-width: 720px; border-top: 1px solid var(--border-strong); font-variant-numeric: tabular-nums; }
  .ln {
    display: grid; grid-template-columns: minmax(0, 1fr) 120px 120px 150px; gap: 20px; align-items: end;
    padding: 18px 0 16px; border-bottom: 1px solid var(--border);
  }
  .ln:last-child { border-bottom: 0; }
  .ln.mid { align-items: center; }
  .ln.quiet { color: var(--muted); }
  .ln { position: relative; }
  .ln.sel { background: var(--raised); box-shadow: -12px 0 0 var(--raised); }
  .ln.sel::before { content: ''; position: absolute; left: -12px; top: 0; bottom: 0; width: 2px; background: var(--text); }
  .c1 { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .ln.mid .c1, .ln:has(.note) .c1 { gap: 4px; }
  .nm { font-size: 16px; font-weight: 500; color: var(--text); overflow-wrap: anywhere; }
  .quiet .nm.strike { color: var(--muted); }
  .strike { text-decoration: line-through; text-decoration-thickness: 1px; font-weight: 400; }
  .sub { color: var(--muted); font-weight: 400; }
  .tog {
    min-height: 36px; margin: -8px 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; font-weight: 500;
    text-align: left; cursor: pointer; overflow-wrap: anywhere;
  }
  .tog:hover { text-decoration: underline; text-underline-offset: 4px; }
  .track { height: 1px; background: var(--border-strong); }
  .fill { height: 2px; margin-top: -0.5px; background: var(--accent); }
  .fill.dash { background: repeating-linear-gradient(90deg, var(--text) 0 4px, transparent 4px 8px); }
  .fill.still { background: var(--muted); }
  .note { color: var(--muted); font-size: 13px; }
  .note a { color: var(--text); }
  .c2 { color: var(--muted); }
  .c3.it { font-style: italic; }
  .c3.bold { font-weight: 600; }
  .c4 { text-align: right; }
  .child { padding: 12px 0 10px 20px; }
  .child .nm { font-size: 14px; font-weight: 400; }
  .child .c1 { gap: 8px; }
  .acts { padding: 10px 0 14px; border-bottom: 1px solid var(--border); background: var(--raised); box-shadow: -12px 0 0 var(--raised); }
  .acts :global(.btn) { border-radius: 0; }
  .empty { padding: 28px 0; color: var(--muted); }
  @media (max-width: 700px) {
    .list { min-width: 0; }
    .ln { grid-template-columns: minmax(0, 1fr) auto; gap: 8px 16px; }
    .c1 { grid-column: 1 / -1; }
    .c2 { display: none; }
    .c4 { text-align: right; }
    .search { flex: 1 1 100%; order: 5; }
    .search input { width: 100%; }
  }
</style>
