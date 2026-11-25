<script lang="ts">
  // Ajustes (shared, Grafito language): general options + Apariencia.
  import AppearancePicker from './AppearancePicker.svelte';
  import SecretsSettings from './SecretsSettings.svelte';
  import { app } from '../lib/store/app.svelte';
  import type { ExistsPolicy, Settings } from '../lib/api/types';

  type Form = {
    download_dir: string;
    max_active: number;
    conns_per_file: number;
    speed_mb: string;
    max_retries: number;
    resume_on_start: boolean;
    exists_policy: ExistsPolicy;
    rotation: boolean;
  };

  function fromSettings(s: Settings | null): Form {
    const d = s?.downloads ?? {};
    return {
      download_dir: s?.download_dir ?? '',
      max_active: Number(d.max_active ?? 3),
      conns_per_file: Number(d.conns_per_file ?? 4),
      speed_mb: d.speed_limit ? String(Math.round((Number(d.speed_limit) / 1e6) * 10) / 10).replace('.', ',') : '',
      max_retries: Number(d.max_retries ?? 5),
      resume_on_start: d.resume_on_start ?? true,
      exists_policy: (d.exists_policy as ExistsPolicy) ?? 'skip',
      rotation: s?.rotation?.enabled ?? true,
    };
  }

  let form = $state<Form>(fromSettings(null));
  let loadedFrom: Settings | null = null;
  let busy = $state(false);

  $effect(() => {
    if (app.settings && app.settings !== loadedFrom) {
      loadedFrom = app.settings;
      form = fromSettings(app.settings);
    }
  });

  async function save(e: Event) {
    e.preventDefault();
    busy = true;
    const speed = Math.max(0, Number(form.speed_mb.replace(',', '.')) || 0);
    await app.saveSettings({
      download_dir: form.download_dir.trim(),
      downloads: {
        ...(app.settings?.downloads ?? {}),
        max_active: Math.max(1, Math.round(form.max_active)),
        conns_per_file: Math.max(1, Math.round(form.conns_per_file)),
        speed_limit: Math.round(speed * 1e6),
        max_retries: Math.max(0, Math.round(form.max_retries)),
        resume_on_start: form.resume_on_start,
        exists_policy: form.exists_policy,
      },
      rotation: { ...(app.settings?.rotation ?? {}), enabled: form.rotation },
    });
    busy = false;
  }
</script>

<header class="head">
  <h1>Ajustes</h1>
  <nav aria-label="Secciones de ajustes" class="toc"><a href="#/ajustes" onclick={(e) => { e.preventDefault(); document.getElementById('aj-general')?.scrollIntoView(); }}>General</a> {#if app.secrets}<a href="#/ajustes" onclick={(e) => { e.preventDefault(); document.getElementById('aj-cred')?.scrollIntoView(); }}>Credenciales</a>{/if} <a href="#/ajustes" onclick={(e) => { e.preventDefault(); document.getElementById('aj-apariencia')?.scrollIntoView(); }}>Apariencia</a></nav>
</header>

<section class="panel pad" aria-labelledby="aj-general">
  <h2 id="aj-general">General</h2>
  {#if !app.settings}
    <p class="muted">Cargando ajustes…</p>
  {:else}
    <form onsubmit={save} class="form">
      <div class="dir">
        <label class="field grow"><span>Carpeta de descargas</span><input class="input mono" bind:value={form.download_dir} required /></label>
        {#if app.isDesktop}
          <button type="button" class="btn" onclick={async () => {
            const p = await app.pickFolder();
            if (p) form.download_dir = p;
          }}>Elegir…</button>
        {/if}
      </div>
      <div class="grid">
        <label class="field"><span>Descargas simultáneas</span><input class="input" type="number" min="1" max="20" bind:value={form.max_active} /></label>
        <label class="field"><span>Conexiones por archivo</span><input class="input" type="number" min="1" max="16" bind:value={form.conns_per_file} /></label>
        <label class="field"><span>Límite de velocidad (MB/s, vacío = sin límite)</span><input class="input" inputmode="decimal" bind:value={form.speed_mb} /></label>
        <label class="field"><span>Reintentos antes de marcar como fallida</span><input class="input" type="number" min="0" max="50" bind:value={form.max_retries} /></label>
        <label class="field">
          <span>Si el archivo ya existe</span>
          <select class="select" bind:value={form.exists_policy}>
            <option value="skip">Saltarlo (mismo tamaño)</option>
            <option value="overwrite">Sobrescribirlo</option>
            <option value="rename">Guardar con otro nombre</option>
          </select>
        </label>
      </div>
      <div class="checks">
        <label class="check"><input type="checkbox" bind:checked={form.resume_on_start} /> Reanudar al arrancar las descargas que estaban activas</label>
        <label class="check"><input type="checkbox" bind:checked={form.rotation} /> Rotar la IP cuando un hoster pone cuota por IP</label>
      </div>
      <div class="end"><button type="submit" class="btn btn-primary" disabled={busy}>{busy ? 'Guardando…' : 'Guardar'}</button></div>
    </form>
  {/if}
</section>

<section class="panel pad" aria-labelledby="aj-clip">
  <h2 id="aj-clip">Portapapeles</h2>
  {#if app.clipboard.available}
    <div class="grid">
      <label class="check"><input type="checkbox" checked={app.clipboard.enabled} onchange={(e) => app.setClipboard((e.currentTarget as HTMLInputElement).checked)} /> Vigilar el portapapeles y detectar enlaces copiados</label>
      <label class="field">
        <span>Al detectar enlaces</span>
        <select class="select" value={app.clipboard.action} onchange={(e) => app.setClipboard(app.clipboard.enabled, (e.currentTarget as HTMLSelectElement).value)}>
          <option value="ask">Preguntar con una notificación</option>
          <option value="add">Añadirlos a la cola</option>
          <option value="linkgrabber">Dejarlos en el LinkGrabber</option>
        </select>
      </label>
    </div>
    <p class="hint">{`El texto copiado solo se procesa en memoria: nunca se guarda ni aparece en los registros.${app.clipboard.backend ? ` Método: ${app.clipboard.backend}.` : ''}`}</p>
  {:else}
    <p class="hint">{app.clipboard.error || 'El monitor del portapapeles funciona en la app de escritorio. En el navegador usa la extensión o pega los enlaces (Ctrl+V en cualquier parte de esta página).'}</p>
  {/if}
</section>

<SecretsSettings />

<section class="panel pad" aria-labelledby="aj-apariencia">
  <h2 id="aj-apariencia">Apariencia</h2>
  <AppearancePicker />
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0 auto 0 0; font-size: 18px; font-weight: 600; letter-spacing: -0.01em; }
  .toc { display: flex; gap: 12px; }
  .toc a { color: var(--muted); min-height: 36px; display: inline-flex; align-items: center; }
  .toc a:hover { color: var(--text); }
  h2 { margin: 0; font-size: 14px; font-weight: 600; }
  .pad { padding: 16px; display: flex; flex-direction: column; gap: 14px; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
  .checks { display: flex; flex-direction: column; }
  .dir { display: flex; align-items: flex-end; gap: 8px; }
  .grow { flex: 1; }
  .end { display: flex; justify-content: flex-end; }
</style>
