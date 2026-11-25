<script lang="ts">
  // Explains the Google Drive per-file quota (PLAN.md 7.1): rotating the IP
  // does not help; what helps is connecting a Drive account so the file is
  // copied to it and the copy is downloaded.
  import { app } from '../lib/store/app.svelte';
  let { compact = false }: { compact?: boolean } = $props();
  const hasDrive = $derived(app.accounts.some((a) => a.hoster === 'gdrive' && a.enabled && a.status === 'ok'));
</script>

<div class="dq" class:compact role="note">
  <p>
    <strong>Rotar la IP no ayuda:</strong> Google Drive limita las descargas de este archivo, no de tu IP.
    {#if hasDrive}
      Con tu cuenta de Drive conectada, reintenta para copiarlo a tu unidad y descargar la copia.
    {:else}
      Conecta una cuenta de Drive para copiarlo a tu unidad y descargar la copia, o espera unas horas.
    {/if}
  </p>
  {#if !hasDrive}
    <a class="btn btn-sm" href="#/cuentas?alta=gdrive">Conectar cuenta de Drive</a>
  {/if}
</div>

<style>
  .dq { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; color: var(--text-2); }
  .dq p { margin: 0; flex: 1 1 320px; }
  .dq strong { color: var(--text); font-weight: 600; }
  .compact { font-size: 0.95em; }
</style>
