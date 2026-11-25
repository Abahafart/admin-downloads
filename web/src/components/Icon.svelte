<script lang="ts" module>
  // Stroke SVG icons (24x24 grid, currentColor). Never emoji.
  export const ICONS = {
    download: ['M12 4v11', 'M7 10l5 5 5-5', 'M5 20h14'],
    pause: ['M9 5v14', 'M15 5v14'],
    play: ['M7 5l12 7-12 7z'],
    retry: ['M4 12a8 8 0 0 1 14-5.3', 'M18 3v4h-4', 'M20 12a8 8 0 0 1-14 5.3', 'M6 21v-4h4'],
    restart: ['M3 12a9 9 0 1 0 3-6.7', 'M3 4v5h5'],
    trash: ['M4 7h16', 'M9 7V4h6v3', 'M6 7l1 13h10l1-13'],
    rotate: ['M21 12a9 9 0 0 1-15.5 6.2', 'M3 12a9 9 0 0 1 15.5-6.2', 'M18 2v4h-4', 'M6 22v-4h4'],
    search: ['M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z', 'M20 20l-3.5-3.5'],
    'chevron-down': ['M6 9l6 6 6-6'],
    'chevron-right': ['M9 6l6 6-6 6'],
    folder: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z'],
    file: ['M6 3h8l4 4v14H6z', 'M14 3v4h4'],
    plus: ['M12 5v14', 'M5 12h14'],
    x: ['M6 6l12 12', 'M18 6L6 18'],
    check: ['M5 12l5 5 9-10'],
    link: ['M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1', 'M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1'],
    key: ['M14 10a4 4 0 1 0-1.2 2.8L20 20', 'M17 17l2-2'],
    globe: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M3 12h18', 'M12 3c3 3 3 15 0 18', 'M12 3c-3 3-3 15 0 18'],
    settings: ['M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z', 'M4 12h2', 'M18 12h2', 'M12 4v2', 'M12 18v2', 'M6.3 6.3l1.4 1.4', 'M16.3 16.3l1.4 1.4', 'M6.3 17.7l1.4-1.4', 'M16.3 7.7l1.4-1.4'],
    upload: ['M12 20V9', 'M7 14l5-5 5 5', 'M5 4h14'],
    command: ['M9 6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3z'],
    help: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .8-1 1.5V14', 'M12 17.5v.01'],
    clipboard: ['M9 4h6v3H9z', 'M9 5H6v16h12V5h-3'],
    alert: ['M12 3l10 18H2z', 'M12 10v4', 'M12 17.5v.01'],
    info: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M12 11v6', 'M12 7.5v.01'],
    user: ['M12 4a4 4 0 1 0 0 8 4 4 0 0 0 0-8z', 'M4 21a8 8 0 0 1 16 0'],
    shield: ['M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z'],
    lock: ['M6 11h12v10H6z', 'M8 11V8a4 4 0 0 1 8 0v3', 'M12 15v2'],
    menu: ['M4 7h16', 'M4 12h16', 'M4 17h16'],
  } satisfies Record<string, string[]>;
  export type IconName = keyof typeof ICONS;
</script>

<script lang="ts">
  let { name, size = 16, stroke = 2, label }: { name: IconName; size?: number; stroke?: number; label?: string } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width={stroke}
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden={label ? undefined : 'true'}
  role={label ? 'img' : undefined}
  aria-label={label}
  focusable="false"
  class="icon"
>
  {#each ICONS[name] as d (d)}<path {d} />{/each}
</svg>

<style>
  .icon { flex: none; }
</style>
