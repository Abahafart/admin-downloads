<script lang="ts">
  // Expand/collapse button of a folder package (a real <button> with
  // aria-expanded; the whole name is the target).
  import Icon from './Icon.svelte';
  import { app } from '../lib/store/app.svelte';
  let { packageId, name, controls }: { packageId: number; name: string; controls?: string } = $props();
  const open = $derived(app.isExpanded(packageId));
</script>

<button
  type="button"
  class="toggle"
  aria-expanded={open}
  aria-controls={controls}
  onclick={(e) => {
    e.stopPropagation();
    app.toggleExpanded(packageId, !open);
  }}
>
  <span class="chev"><Icon name={open ? 'chevron-down' : 'chevron-right'} size={12} stroke={2.4} /></span>
  <span class="name">{name}</span>
  <span class="sr-only">{open ? '(plegar carpeta)' : '(desplegar carpeta)'}</span>
</button>

<style>
  .toggle {
    display: inline-flex; align-items: center; gap: 8px; min-height: 36px; padding: 0; margin: -8px 0;
    border: 0; background: transparent; color: var(--text); cursor: pointer; text-align: left; font-weight: 500;
  }
  .chev { color: var(--muted); display: inline-flex; }
  .name { overflow-wrap: anywhere; }
</style>
