<script lang="ts">
  // The network exit of a download: Directa, Tor, WireGuard, Proxy SOCKS5…
  // ("Tor → rotando" while that egress rotates).
  import { app } from '../lib/store/app.svelte';
  let { id, fallback = '' }: { id: string | undefined; fallback?: string } = $props();
  const text = $derived(app.egressLabel(id) || fallback);
</script>

{#if text}<span class="egress" title={id}>{text}</span>{/if}
