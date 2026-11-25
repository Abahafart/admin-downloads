<script lang="ts">
  import { app } from '../lib/store/app.svelte';
  let { rowKey, name = '', small = true }: { rowKey: string; name?: string; small?: boolean } = $props();
</script>

<button
  type="button"
  class="btn {small ? 'btn-sm' : ''} retry"
  aria-label={name ? `Reintentar ${name}` : 'Reintentar'}
  onclick={(e) => {
    e.stopPropagation();
    void app.retry(rowKey);
  }}>Reintentar</button
>

<style>
  .retry { min-height: 36px; }
</style>
