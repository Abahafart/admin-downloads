<script lang="ts">
  // Field to add links: paste or drop text/URLs (also files with links).
  // Every URL in the text is extracted by the server (AddLinksRequest.text).
  import { app } from '../lib/store/app.svelte';
  let {
    compact = false,
    autofocus = false,
    onadded,
    id = 'add-links',
  }: { compact?: boolean; autofocus?: boolean; onadded?: () => void; id?: string } = $props();
  let text = $state('');
  let busy = $state(false);
  let over = $state(false);
  /** How many of the pasted links the daemon already has (POST /links/known). */
  let known = $state(0);
  let checkSeq = 0;
  const knownText = $derived(
    known === 0 ? '' : known === 1 ? 'Uno de estos enlaces ya está en la cola, descargado o en el LinkGrabber.' : `${known} de estos enlaces ya están en la cola, descargados o en el LinkGrabber.`,
  );

  // Debounced check while typing/pasting: warns before duplicating a
  // download (the links are still added if the user confirms).
  $effect(() => {
    const urls = [...new Set(text.match(/https?:\/\/[^\s<>"')]+/g) ?? [])].slice(0, 1000);
    const seq = ++checkSeq;
    if (!urls.length) {
      known = 0;
      return;
    }
    const t = setTimeout(async () => {
      const k = await app.knownLinks(urls);
      if (seq === checkSeq) known = k.length;
    }, 400);
    return () => clearTimeout(t);
  });

  async function submit(e?: Event) {
    e?.preventDefault();
    if (!text.trim() || busy) return;
    busy = true;
    const r = await app.addLinks(text);
    busy = false;
    if (r) {
      text = '';
      onadded?.();
    }
  }

  async function drop(e: DragEvent) {
    e.preventDefault();
    e.stopPropagation();
    over = false;
    const dt = e.dataTransfer;
    if (!dt) return;
    let t = dt.getData('text/uri-list') || dt.getData('text/plain');
    if (!t && dt.files.length) t = (await Promise.all([...dt.files].map((f) => f.text()))).join('\n');
    if (t) text = (text ? text + '\n' : '') + t;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) void submit();
  }
</script>

<form class="add" class:compact class:over onsubmit={submit}>
  <label for={id} class={compact ? 'sr-only' : 'field-label'}>Enlaces o texto con enlaces</label>
  <!-- svelte-ignore a11y_autofocus -->
  <textarea
    {id}
    class="textarea"
    rows={compact ? 2 : 4}
    placeholder="Pega aquí enlaces de MEGA, MediaFire, Google Drive, TeraBox… o arrastra texto o un archivo"
    bind:value={text}
    onkeydown={keydown}
    ondragover={(e) => {
      e.preventDefault();
      over = true;
    }}
    ondragleave={() => (over = false)}
    ondrop={drop}
    {autofocus}
  ></textarea>
  <p class="known" aria-live="polite">{knownText}</p>
  <div class="row">
    <p class="hint">Ctrl+Intro para añadir. Las carpetas se expanden en el LinkGrabber.</p>
    <button type="submit" class="btn btn-primary" disabled={!text.trim() || busy}>{busy ? 'Añadiendo…' : 'Añadir enlaces'}</button>
  </div>
</form>

<style>
  .add { display: flex; flex-direction: column; gap: 8px; }
  .row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 12px; }
  .over .textarea { border-color: var(--accent); border-style: dashed; }
  .compact .hint { display: none; }
  .known { margin: 0; color: var(--warn); font-size: 0.92em; }
  /* Stays in the tree (live region) but takes no room while empty. */
  .known:empty { margin-top: -8px; }
</style>
