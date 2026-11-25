<script lang="ts">
  let { title = 'El servidor no admite reanudar: si se corta, empieza desde cero' }: { title?: string } = $props();
</script>

<span class="badge" {title}>no reanudable</span>
