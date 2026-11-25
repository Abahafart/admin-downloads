<script lang="ts">
  // Mouse equivalent of p / r / R / i / Supr for the selected row in the
  // Medianoche, Monocromo and Enfoque views (shown under the selected card or
  // line). Plain .btn buttons: every style restyles them with its CSS.
  import { app } from '../../lib/store/app.svelte';
  import Icon from '../../components/Icon.svelte';
  import type { RowVM } from './model';
  let { vm, icons = false }: { vm: RowVM; icons?: boolean } = $props();
  const name = $derived(vm.kind === 'package' ? vm.name + '/' : vm.name);
  const stop = (fn: () => unknown) => (e: MouseEvent) => {
    e.stopPropagation();
    void fn();
  };
</script>

<div class="ra" role="toolbar" aria-label="Acciones sobre {name}">
  {#if vm.st.canResume}
    <button type="button" class="btn btn-sm" aria-keyshortcuts="p" onclick={stop(() => app.resume(vm.key))}>{#if icons}<Icon name="play" size={14} />{/if}Reanudar</button>
  {:else if vm.st.canPause}
    <button type="button" class="btn btn-sm" aria-keyshortcuts="p" onclick={stop(() => app.pause(vm.key))}>{#if icons}<Icon name="pause" size={14} />{/if}Pausar</button>
  {/if}
  {#if vm.st.canRetry}
    <button type="button" class="btn btn-sm" aria-keyshortcuts="r" onclick={stop(() => app.retry(vm.key))}>{#if icons}<Icon name="retry" size={14} />{/if}Reintentar</button>
  {/if}
  <button type="button" class="btn btn-sm" aria-keyshortcuts="Shift+R" onclick={stop(() => app.restart(vm.key))}>{#if icons}<Icon name="restart" size={14} />{/if}Reiniciar</button>
  {#if vm.kind === 'download' && vm.st.state === 'waiting' && !vm.st.fileQuota}
    <button type="button" class="btn btn-sm" aria-keyshortcuts="i" onclick={stop(() => app.rotate())}>{#if icons}<Icon name="rotate" size={14} />{/if}Rotar IP</button>
  {/if}
  {#if vm.kind === 'package' && vm.pkg?.source_url}
    <button type="button" class="btn btn-sm" onclick={stop(() => app.rescan(vm.packageId))}>{#if icons}<Icon name="search" size={14} />{/if}Buscar nuevos</button>
  {/if}
  <button type="button" class="btn btn-sm btn-danger" aria-label="Eliminar {name}" aria-keyshortcuts="Delete" onclick={stop(() => app.askDelete(vm.key))}>{#if icons}<Icon name="trash" size={14} />{/if}Eliminar</button>
</div>

<style>
  .ra { display: flex; flex-wrap: wrap; gap: 6px; }
</style>
