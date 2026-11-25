<script lang="ts">
  // LinkGrabber (shared, Grafito language): pasted links expand into
  // packages; folders show a tree with checkboxes for files and subfolders,
  // the total size, filters by extension / size / regex, destination and
  // exists policy. The tree grows while the folder is being listed.
  import { SvelteSet } from 'svelte/reactivity';
  import AddLinks from '../components/AddLinks.svelte';
  import Icon from '../components/Icon.svelte';
  import { indeterminate } from '../components/indeterminate';
  import { app } from '../lib/store/app.svelte';
  import { formatBytes, plural } from '../lib/store/format';
  import { hosterName } from '../lib/store/labels';
  import {
    buildTree, compileFilter, confirmItem, folderCheck, isFilterActive, selectedFiles, sumSize, toggleFolder,
    type FileFilter, type TreeFolder,
  } from '../lib/store/tree';
  import type { ConfirmItem, ExistsPolicy, GrabberEntry, GrabberFile } from '../lib/api/types';

  const EMPTY = new Set<number>();
  let excluded = $state<Record<number, SvelteSet<number>>>({});
  let skip = $state<Record<number, boolean>>({});
  let names = $state<Record<number, string>>({});
  let collapsed = $state<Record<string, boolean>>({});
  let filterUI = $state({ ext: '', min: '', max: '', regex: '' });
  let dest = $state('');
  let policy = $state<ExistsPolicy | ''>('');
  let paused = $state(false);
  let busy = $state(false);

  const filter = $derived<FileFilter>({
    ext: filterUI.ext,
    minSize: Number(filterUI.min.replace(',', '.')) * 1e6 || 0,
    maxSize: Number(filterUI.max.replace(',', '.')) * 1e6 || 0,
    regex: filterUI.regex,
  });
  const compiled = $derived(compileFilter(filter));
  const entries = $derived(app.grabberEntries);
  const ex = (id: number): Set<number> => excluded[id] ?? EMPTY;
  const exMut = (id: number): SvelteSet<number> => (excluded[id] ??= new SvelteSet<number>());

  const ready = $derived(entries.filter((e) => e.state === 'ready' && !skip[e.id]));
  const resolving = $derived(entries.filter((e) => e.state === 'pending' || e.state === 'resolving').length);
  const items = $derived(ready.map((e) => confirmItem(e, ex(e.id), filter, names[e.id]?.trim())).filter((i): i is ConfirmItem => !!i));
  const totals = $derived.by(() => {
    const files = ready.flatMap((e) => selectedFiles(e, ex(e.id), filter));
    return { count: files.length, ...sumSize(files) };
  });

  function treeOf(e: GrabberEntry): TreeFolder {
    return buildTree(e.files);
  }

  function toggleFile(e: GrabberEntry, f: GrabberFile) {
    const s = exMut(e.id);
    if (s.has(f.index)) s.delete(f.index);
    else s.add(f.index);
  }

  function setAll(e: GrabberEntry, on: boolean) {
    const s = exMut(e.id);
    s.clear();
    if (!on) for (const f of e.files ?? []) s.add(f.index);
  }

  async function confirm() {
    if (!items.length) return;
    busy = true;
    const ok = await app.confirmGrabber({ items, dest_dir: dest.trim() || undefined, exists_policy: policy, paused });
    busy = false;
    if (ok) app.go('descargas');
  }

  function stateText(e: GrabberEntry): string {
    const n = e.files?.length ?? 0;
    switch (e.state) {
      case 'pending':
        return 'En espera de listar';
      case 'resolving':
        return e.is_folder ? `Listando… ${plural(n, 'archivo', 'archivos')}` : 'Comprobando…';
      case 'ready':
        return e.is_folder ? plural(n, 'archivo', 'archivos') : 'Listo';
      case 'error':
        return 'Error';
      case 'unsupported':
        return 'No soportado';
    }
  }
</script>

<header class="head">
  <h1>LinkGrabber</h1>
  <span class="muted">{plural(entries.length, 'enlace', 'enlaces')}</span>
  {#if entries.length}
    <button type="button" class="btn" onclick={() => app.removeGrabber(entries.map((e) => e.id))}>Vaciar</button>
  {/if}
</header>

<section class="panel pad" aria-label="Añadir enlaces">
  <AddLinks id="lg-add" />
</section>

{#if entries.length}
  <section class="panel pad filters" aria-labelledby="lg-filtros">
    <h2 id="lg-filtros">Filtros</h2>
    <div class="grid">
      <label class="field"><span>Extensiones</span><input class="input" placeholder="mkv, mp4" bind:value={filterUI.ext} /></label>
      <label class="field"><span>Tamaño mínimo (MB)</span><input class="input" inputmode="decimal" placeholder="0" bind:value={filterUI.min} /></label>
      <label class="field"><span>Tamaño máximo (MB)</span><input class="input" inputmode="decimal" placeholder="sin límite" bind:value={filterUI.max} /></label>
      <label class="field"><span>Expresión regular sobre la ruta</span><input class="input mono" placeholder="Temporada 1/.*" bind:value={filterUI.regex} aria-invalid={!!compiled.error} /></label>
    </div>
    {#if compiled.error}<p class="error-text" role="alert">{compiled.error}</p>{/if}
    {#if isFilterActive(filter)}
      <div class="row">
        <p class="hint">Los archivos que no pasan el filtro no se descargan.</p>
        <button type="button" class="btn btn-sm" onclick={() => (filterUI = { ext: '', min: '', max: '', regex: '' })}>Quitar filtros</button>
      </div>
    {/if}
  </section>

  {#each entries as e (e.id)}
    {@const sel = selectedFiles(e, ex(e.id), filter)}
    {@const all = e.files?.length ?? 0}
    {@const tot = sumSize(sel)}
    <section class="panel entry" class:off={skip[e.id]} aria-labelledby="lg-e-{e.id}">
      <div class="ehead">
        <label class="check">
          <input
            type="checkbox"
            checked={!skip[e.id] && sel.length > 0}
            use:indeterminate={!skip[e.id] && sel.length > 0 && sel.length < all}
            disabled={e.state === 'error' || e.state === 'unsupported'}
            onchange={(ev) => {
              const on = (ev.currentTarget as HTMLInputElement).checked;
              skip[e.id] = !on;
              if (on && sel.length === 0) setAll(e, true);
            }}
          />
          <span class="sr-only">Incluir {e.package_name}</span>
        </label>
        <span class="ic muted"><Icon name={e.is_folder ? 'folder' : 'file'} /></span>
        <h2 id="lg-e-{e.id}" class="sr-only">{e.package_name}</h2>
        <label class="pname">
          <span class="sr-only">Nombre del paquete</span>
          <input class="input" value={names[e.id] ?? e.package_name} oninput={(ev) => (names[e.id] = (ev.currentTarget as HTMLInputElement).value)} />
        </label>
        <span class="muted">{hosterName(e.hoster)}</span>
        <span class:tone-err={e.state === 'error' || e.state === 'unsupported'} class:muted={e.state !== 'error'} aria-live="polite">{stateText(e)}</span>
        <span class="tabular size">{formatBytes(tot.size)}{tot.unknown ? '+' : ''}{#if all > 1}<span class="muted"> · {sel.length}/{all}</span>{/if}</span>
        <button type="button" class="btn btn-icon btn-ghost" aria-label="Quitar {e.package_name}" onclick={() => app.removeGrabber([e.id])}><Icon name="x" /></button>
      </div>
      <p class="url muted mono" title={e.url}>{e.url}</p>
      {#if e.error}<p class="error-text">{e.error}</p>{/if}
      {#if e.is_folder && all > 0}
        <ul class="tree" aria-label="Archivos de {e.package_name}">
          {@render folder(e, treeOf(e), 0)}
        </ul>
        {#if e.state === 'resolving'}<p class="hint pad-l">Se siguen añadiendo archivos mientras se lista la carpeta…</p>{/if}
      {:else if !e.is_folder && all > 0}
        <ul class="tree" aria-label="Archivo">
          {#each e.files ?? [] as f (f.index)}{@render fileItem(e, f, 0)}{/each}
        </ul>
      {/if}
    </section>
  {/each}

  <section class="panel pad confirm" aria-label="Añadir a la cola">
    <div class="grid">
      <div class="field wide dest">
        <label class="field grow">
          <span>Carpeta de destino</span>
          <input class="input mono" bind:value={dest} placeholder={app.settings?.download_dir ?? 'Carpeta de descargas por defecto'} />
        </label>
        {#if app.isDesktop}
          <button type="button" class="btn" onclick={async () => {
            const p = await app.pickFolder();
            if (p) dest = p;
          }}>Elegir…</button>
        {/if}
      </div>
      <label class="field">
        <span>Si el archivo ya existe</span>
        <select class="select" bind:value={policy}>
          <option value="">Lo que digan los ajustes</option>
          <option value="skip">Saltarlo (mismo tamaño)</option>
          <option value="overwrite">Sobrescribirlo</option>
          <option value="rename">Guardar con otro nombre</option>
        </select>
      </label>
      <label class="check"><input type="checkbox" bind:checked={paused} /> Añadir en pausa</label>
    </div>
    <div class="row">
      <p class="tabular" aria-live="polite">
        <strong>{plural(totals.count, 'archivo', 'archivos')}</strong> · {formatBytes(totals.size)}{totals.unknown ? ' + tamaño desconocido' : ''}
        {#if resolving}<span class="muted"> · {plural(resolving, 'enlace', 'enlaces')} todavía listándose</span>{/if}
      </p>
      <button type="button" class="btn btn-primary" disabled={!items.length || busy} onclick={confirm}>
        <Icon name="download" /> {busy ? 'Añadiendo…' : 'Descargar'}
      </button>
    </div>
  </section>
{:else}
  <p class="muted empty">No hay enlaces pendientes. Pega texto con enlaces arriba, arrástralo a la ventana o usa <kbd>Ctrl</kbd> <kbd>K</kbd>.</p>
{/if}

{#snippet folder(e: GrabberEntry, node: TreeFolder, depth: number)}
  {#each node.folders as f (f.path)}
    {@const ck = folderCheck(f, ex(e.id))}
    {@const ckey = `${e.id}:${f.path}`}
    <li>
      <div class="node" style:--d={depth}>
        <button type="button" class="btn btn-icon btn-ghost fold" aria-expanded={!collapsed[ckey]} aria-label="{collapsed[ckey] ? 'Desplegar' : 'Plegar'} {f.name}" onclick={() => (collapsed[ckey] = !collapsed[ckey])}>
          <Icon name={collapsed[ckey] ? 'chevron-right' : 'chevron-down'} size={12} stroke={2.4} />
        </button>
        <label class="check grow">
          <input type="checkbox" checked={ck === 'all'} use:indeterminate={ck === 'some'} onchange={() => toggleFolder(f, exMut(e.id))} />
          <Icon name="folder" size={14} />
          <span class="fname">{f.name}/</span>
        </label>
        <span class="muted tabular">{plural(f.all.length, 'archivo', 'archivos')} · {formatBytes(f.size)}{f.unknownSize ? '+' : ''}</span>
      </div>
      {#if !collapsed[ckey]}
        <ul>{@render folder(e, f, depth + 1)}</ul>
      {/if}
    </li>
  {/each}
  {#each node.files as f (f.index)}{@render fileItem(e, f, depth)}{/each}
{/snippet}

{#snippet fileItem(e: GrabberEntry, f: GrabberFile, depth: number)}
  {@const passes = compiled.error ? true : compiled.test(f)}
  <li>
    <div class="node file" class:filtered={!passes} style:--d={depth}>
      <span class="fold-sp"></span>
      <label class="check grow">
        <input type="checkbox" checked={!ex(e.id).has(f.index) && passes} disabled={!passes} onchange={() => toggleFile(e, f)} />
        <span class="fname">{f.name}</span>
        {#if f.known}<span class="badge">ya en la cola o descargado</span>{/if}
        {#if !passes}<span class="badge">fuera del filtro</span>{/if}
      </label>
      <span class="muted tabular">{formatBytes(f.size)}</span>
    </div>
  </li>
{/snippet}

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0; font-size: 18px; font-weight: 600; letter-spacing: -0.01em; }
  .head .muted { margin-right: auto; }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .pad { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; align-items: end; }
  .grid .wide { grid-column: span 2; }
  @media (max-width: 520px) { .grid .wide { grid-column: auto; } }
  .row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 16px; }
  .row p { margin: 0; }
  .entry { padding: 8px 16px 12px; display: flex; flex-direction: column; gap: 4px; }
  .entry.off { opacity: 0.75; }
  .ehead { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
  .ic { display: inline-flex; }
  .pname { flex: 1 1 220px; min-width: 0; }
  .pname .input { font-weight: 500; border-color: transparent; background: transparent; padding: 0 6px; }
  .pname .input:hover, .pname .input:focus { border-color: var(--border-strong); background: var(--bg); }
  .size { min-width: 90px; text-align: right; }
  .url { margin: 0 0 4px 32px; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tree, .tree ul { list-style: none; margin: 0; padding: 0; }
  .tree { border-top: 1px solid var(--border); }
  .node { display: flex; align-items: center; gap: 6px; padding-left: calc(var(--d) * 22px); border-bottom: 1px solid var(--border); min-height: 38px; }
  .tree > li:last-child > .node, .tree li:last-child > ul > li:last-child > .node { border-bottom: 0; }
  .grow { flex: 1; min-width: 0; }
  .fname { overflow-wrap: anywhere; }
  .fold { min-height: 36px; }
  .fold-sp { width: 36px; flex: none; }
  .file .grow { color: var(--text-2); }
  .filtered .grow { color: var(--muted); }
  .pad-l { padding-left: 32px; }
  .confirm { position: sticky; bottom: 0; z-index: 2; }
  .dest { flex-direction: row; align-items: flex-end; gap: 8px; }
  .empty { margin: 0; }
  kbd { font-family: var(--font-mono); font-size: 11px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 0 4px; }
</style>
