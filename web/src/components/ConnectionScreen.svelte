<script lang="ts">
  // Shown while connecting for the first time or when the daemon is down.
  import { app } from '../lib/store/app.svelte';
</script>

<main class="conn" aria-busy={app.status === 'connecting'}>
  <div class="panel card" role="status">
    {#if app.status === 'connecting'}
      <p>Conectando con el servicio de descargas…</p>
    {:else}
      <h1>Sin conexión con el servicio</h1>
      <p class="hint">{app.error || 'El daemon no responde.'} Se reintenta cada pocos segundos.</p>
      <p class="hint">Comprueba que <code class="mono">adesc daemon</code> está en marcha.</p>
    {/if}
  </div>
</main>

<style>
  .conn { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
  .card { width: min(440px, 100%); padding: 24px; display: flex; flex-direction: column; gap: 10px; }
  h1 { margin: 0; font-size: 16px; font-weight: 600; }
  p { margin: 0; }
</style>
