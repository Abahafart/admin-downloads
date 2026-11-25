<script lang="ts">
  import Modal from './Modal.svelte';
  import { app } from '../lib/store/app.svelte';
  import { parseKey } from '../lib/store/rows';
  let files = $state(false);
  const keys = $derived(app.ui.confirmDelete?.keys ?? []);
  const names = $derived(
    keys.map((k) => {
      const r = parseKey(k);
      if (!r) return k;
      return r.kind === 'package' ? (app.pkg(r.id)?.name ?? k) + '/' : (app.download(r.id)?.name ?? k);
    }),
  );
  $effect(() => {
    if (app.ui.confirmDelete) files = false;
  });
</script>

<Modal open={!!app.ui.confirmDelete} title="¿Eliminar de la lista?" onclose={() => (app.ui.confirmDelete = null)} width={460}>
  <p>{names.length === 1 ? `Se quitará «${names[0]}» de la lista.` : `Se quitarán ${names.length} elementos de la lista.`}</p>
  <label class="check"><input type="checkbox" bind:checked={files} /> Borrar también los archivos del disco</label>
  <div class="actions">
    <button type="button" class="btn" onclick={() => (app.ui.confirmDelete = null)}>Cancelar</button>
    <button type="button" class="btn btn-danger" onclick={() => app.remove(keys, files)}>Eliminar</button>
  </div>
</Modal>

<style>
  p { margin: 0; overflow-wrap: anywhere; }
  .actions { display: flex; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
</style>
