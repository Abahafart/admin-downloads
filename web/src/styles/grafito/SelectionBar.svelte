<script lang="ts">
  // Actions on the selected row (mouse equivalent of p / r / R / Supr).
  import Icon from '../../components/Icon.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { packageStatusOf, statusOf } from '../../lib/store/status';
  const it = $derived(app.selectedItem);
  const st = $derived(it ? (it.kind === 'package' ? packageStatusOf(it.pkg, it.children, app.now) : statusOf(it.dl, app.now)) : null);
  const name = $derived(it ? (it.kind === 'package' ? it.pkg.name + '/' : it.dl.name) : '');
</script>

{#if it && st}
  <div class="sel" role="toolbar" aria-label="Acciones sobre {name}">
    <span class="what muted">Seleccionada: <span class="tone-text">{name}</span></span>
    {#if st.canResume}
      <button type="button" class="btn btn-sm" onclick={() => app.resume(it.key)} aria-keyshortcuts="p"><Icon name="play" size={14} /> Reanudar</button>
    {:else if st.canPause}
      <button type="button" class="btn btn-sm" onclick={() => app.pause(it.key)} aria-keyshortcuts="p"><Icon name="pause" size={14} /> Pausar</button>
    {/if}
    {#if st.canRetry}
      <button type="button" class="btn btn-sm" onclick={() => app.retry(it.key)} aria-keyshortcuts="r"><Icon name="retry" size={14} /> Reintentar</button>
    {/if}
    <button type="button" class="btn btn-sm" onclick={() => app.restart(it.key)} aria-keyshortcuts="Shift+R"><Icon name="restart" size={14} /> Reiniciar</button>
    {#if it.kind === 'package' && it.pkg.source_url}
      <button type="button" class="btn btn-sm" onclick={() => app.rescan(it.pkg.id)}><Icon name="search" size={14} /> Buscar nuevos</button>
    {/if}
    <button type="button" class="btn btn-sm btn-icon btn-danger" aria-label="Eliminar {name}" aria-keyshortcuts="Delete" onclick={() => app.askDelete(it.key)}><Icon name="trash" size={14} /></button>
  </div>
{/if}

<style>
  .sel { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .what { margin-right: 4px; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
