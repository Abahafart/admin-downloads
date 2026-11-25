<script lang="ts">
  // Ajustes › Apariencia: style (with thumbnails), tema and accent. Changes
  // apply instantly, are mirrored to localStorage (no flash on reload) and
  // saved in settings.appearance. Accents that fail contrast in the active
  // scheme are disabled with the reason.
  import { app } from '../lib/store/app.svelte';
  import { appearance } from '../theme/runtime.svelte';
  import { STYLE_LIST } from '../styles/registry';
  import StyleThumb from '../styles/StyleThumb.svelte';
  import type { Tema } from '../theme';

  const TEMAS: { id: Tema; label: string }[] = [
    { id: 'sistema', label: 'Sistema' },
    { id: 'oscuro', label: 'Oscuro' },
    { id: 'claro', label: 'Claro' },
  ];
  const chosen = $derived(appearance.pref.acento);
  const effective = $derived(appearance.accent.accent);
  const fellBack = $derived(!!chosen && chosen !== effective);
</script>

<div class="ap">
  <fieldset>
    <legend>Estilo</legend>
    <div class="styles">
      {#each STYLE_LIST as s (s.id)}
        <label class="style" class:on={appearance.pref.estilo === s.id}>
          <input type="radio" name="estilo" value={s.id} checked={appearance.pref.estilo === s.id} onchange={() => app.setAppearance({ estilo: s.id, acento: '' })} />
          {#if s.Thumbnail}<s.Thumbnail esquema={appearance.esquema} />{:else}<StyleThumb style={s} esquema={appearance.esquema} />{/if}
          <span class="sname">{s.letra} · {s.nombre}</span>
          <span class="sdesc">{s.descripcion}</span>
        </label>
      {/each}
    </div>
  </fieldset>

  <fieldset>
    <legend>Tema</legend>
    <div class="radios">
      {#each TEMAS as t (t.id)}
        <label class="check opt" class:on={appearance.pref.tema === t.id}>
          <input type="radio" name="tema" value={t.id} checked={appearance.pref.tema === t.id} onchange={() => app.setAppearance({ tema: t.id })} />
          {t.label}{#if t.id === 'sistema'}<span class="muted"> (ahora {appearance.system})</span>{/if}
        </label>
      {/each}
    </div>
  </fieldset>

  <fieldset>
    <legend>Acento</legend>
    <div class="accents">
      <label class="check opt" class:on={!chosen}>
        <input type="radio" name="acento" value="" checked={!chosen} onchange={() => app.setAppearance({ acento: '' })} />
        <span class="sw" style:background={appearance.theme[appearance.esquema].accent}></span>
        Por defecto
      </label>
      {#each appearance.accents as a (a.accent)}
        <label class="check opt" class:on={chosen === a.accent} class:dis={!a.ok}>
          <input
            type="radio"
            name="acento"
            value={a.accent}
            checked={chosen === a.accent}
            disabled={!a.ok}
            aria-describedby={a.ok ? undefined : `acc-why-${a.accent.slice(1)}`}
            onchange={() => app.setAppearance({ acento: a.accent })}
          />
          <span class="sw" style:background={a.accent}></span>
          <span class="mono">{a.accent}</span>
          {#if !a.ok}<span class="why" id="acc-why-{a.accent.slice(1)}">No disponible: {a.reason}</span>{/if}
        </label>
      {/each}
    </div>
    {#if fellBack}
      <p class="hint" role="status">El acento elegido ({chosen}) no tiene contraste suficiente en el tema {appearance.esquema}: se usa el de por defecto.</p>
    {/if}
  </fieldset>
</div>

<style>
  .ap { display: flex; flex-direction: column; gap: 20px; }
  fieldset { margin: 0; padding: 0; border: 0; min-width: 0; }
  legend { padding: 0; margin-bottom: 10px; font-weight: 600; }
  .styles { display: grid; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 12px; }
  .style {
    position: relative; display: flex; flex-direction: column; gap: 6px; padding: 8px; cursor: pointer;
    border: 1px solid var(--border); border-radius: var(--radius-lg); background: var(--surface);
  }
  .style.on { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
  .style input { position: absolute; opacity: 0; pointer-events: none; }
  .style:has(input:focus-visible) { outline: 2px solid var(--accent); outline-offset: 2px; }
  .sname { font-weight: 600; }
  .sdesc { color: var(--muted); font-size: 12px; }
  .radios, .accents { display: flex; flex-wrap: wrap; gap: 8px; }
  .opt { padding: 0 12px; border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface); flex-wrap: wrap; }
  .opt.on { border-color: var(--accent); }
  .opt.dis { cursor: not-allowed; color: var(--muted); }
  .sw { width: 16px; height: 16px; border-radius: var(--radius-sm); border: 1px solid var(--border-strong); flex: none; }
  .why { flex-basis: 100%; font-size: 12px; color: var(--muted); padding-bottom: 6px; }
  .hint { margin-top: 8px; }
</style>
