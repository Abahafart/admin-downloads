<script lang="ts">
  // Red y rotación (shared, Grafito language): every egress with kind, IP,
  // health and per-hoster exhaustion countdowns; import proxies, add a
  // WireGuard .conf, test, rotate. Router / system VPN commands are shown
  // read-only (they are only editable in config.toml).
  import Icon from '../components/Icon.svelte';
  import { app } from '../lib/store/app.svelte';
  import { formatAgo, formatCountdown } from '../lib/store/format';
  import { EGRESS_KIND, hosterName } from '../lib/store/labels';
  import type { EgressStatus } from '../lib/api/types';

  let proxies = $state('');
  let wgName = $state('');
  let wgConf = $state('');
  let testing = $state<Record<string, boolean>>({});

  const list = $derived(
    [...app.egressList].sort((a, b) => (a.scope === b.scope ? 0 : a.scope === 'global' ? 1 : -1) || a.id.localeCompare(b.id)),
  );
  // Read-only summary (api.Settings.network): commands live in config.toml.
  const router = $derived(app.settings?.network?.router);
  const vpns = $derived(app.settings?.network?.system_vpn ?? []);
  const rotationText = $derived.by(() => {
    const r = app.settings?.rotation;
    if (!r) return '';
    const parts = [`Rotación ${r.enabled ? 'activada' : 'desactivada'}`];
    if (r.enabled && r.strategy) parts.push(`estrategia ${r.strategy}`);
    if (r.enabled && r.global_rotation) parts.push('si todas las salidas se agotan se usa el router o la VPN del sistema');
    return parts.join(' · ') + '.';
  });

  function exhausted(e: EgressStatus): { hoster: string; left: string }[] {
    return Object.entries(e.exhausted ?? {})
      .filter(([, until]) => Date.parse(until) > app.now)
      .map(([h, until]) => ({ hoster: hosterName(h), left: formatCountdown(until, app.now) }));
  }

  async function test(id: string) {
    testing[id] = true;
    await app.testEgress(id);
    testing[id] = false;
  }

  async function testAll() {
    await Promise.all(list.filter((e) => e.scope !== 'global').map((e) => test(e.id)));
  }

  async function readFile(e: Event, set: (t: string, name: string) => void) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (f) set(await f.text(), f.name);
  }

  async function importProxies(e: Event) {
    e.preventDefault();
    if (await app.importProxies(proxies)) proxies = '';
  }

  async function addWG(e: Event) {
    e.preventDefault();
    if (await app.addWireGuard(wgName.trim() || 'wireguard', wgConf)) {
      wgName = '';
      wgConf = '';
    }
  }
</script>

<header class="head">
  <h1>Red y rotación</h1>
  <span class="muted">IP pública <span class="tone-text tabular">{app.publicIP || '—'}</span> · Salida <span class="tone-text">{app.currentEgressLabel}</span></span>
  <button type="button" class="btn" onclick={testAll}>Probar todas</button>
  <button type="button" class="btn btn-primary rot" onclick={() => app.rotate('')} aria-keyshortcuts="i" disabled={!!app.data.rotating['']}>
    <Icon name="rotate" /> {app.data.rotating[''] ? 'Rotando…' : 'Rotar IP ahora'}
  </button>
</header>

{#if rotationText}
  <p class="hint">{rotationText}</p>
{/if}

<div class="table-wrap">
  <table class="table eg">
    <thead>
      <tr>
        <th scope="col">Salida</th>
        <th scope="col">Tipo</th>
        <th scope="col">IP pública</th>
        <th scope="col">Estado</th>
        <th scope="col">En uso</th>
        <th scope="col">Agotada para</th>
        <th scope="col"><span class="sr-only">Acciones</span></th>
      </tr>
    </thead>
    <tbody>
      {#each list as e (e.id)}
        {@const ex = exhausted(e)}
        {@const rot = !!app.data.rotating[e.id]}
        <tr>
          <td>
            <div class="who"><span>{e.label || e.id}</span>{#if e.label && e.label !== e.id}<span class="muted mono small">{e.id}</span>{/if}</div>
          </td>
          <td class="muted">{EGRESS_KIND[e.kind] ?? e.kind}{#if e.scope === 'global'}<span class="badge g">global</span>{/if}</td>
          <td class="tabular">{e.public_ip || '—'}</td>
          <td>
            <div class="who">
              {#if rot}
                <span class="tone-warn">Rotando…</span>
              {:else if e.healthy}
                <span>Funciona</span>
              {:else}
                <span class="tone-err">Caída{e.last_error ? ` · ${e.last_error}` : ''}</span>
              {/if}
              <span class="muted small">Probada {formatAgo(e.checked_at, app.now)}</span>
            </div>
          </td>
          <td class="tabular" class:muted={!e.in_use}>{e.in_use || '—'}</td>
          <td>
            {#if ex.length}
              <ul class="ex">
                {#each ex as x (x.hoster)}<li><span>{x.hoster}</span> <span class="tone-warn tabular">· {x.left}</span></li>{/each}
              </ul>
            {:else}
              <span class="muted">—</span>
            {/if}
          </td>
          <td>
            <div class="acts">
              {#if e.scope !== 'global'}
                <button type="button" class="btn btn-sm" disabled={testing[e.id]} onclick={() => test(e.id)}>{testing[e.id] ? 'Probando…' : 'Probar'}</button>
              {/if}
              {#if e.kind !== 'direct' && e.kind !== 'http' && e.kind !== 'socks5'}
                <button type="button" class="btn btn-sm" disabled={rot} onclick={() => app.rotate(e.id)}>Rotar</button>
              {/if}
            </div>
          </td>
        </tr>
      {:else}
        <tr><td colspan="7" class="muted">No hay salidas configuradas: se usa la conexión directa.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<div class="cols">
  <form class="panel pad" onsubmit={importProxies} aria-labelledby="net-px">
    <h2 id="net-px">Importar proxies</h2>
    <label class="field">
      <span>Uno por línea: <code class="mono">host:puerto</code>, <code class="mono">socks5://usuario:clave@host:puerto</code> o <code class="mono">http://host:puerto</code></span>
      <textarea class="textarea" rows="5" bind:value={proxies} placeholder="socks5://192.0.2.10:1080&#10;http://198.51.100.20:8080"></textarea>
    </label>
    <label class="field"><span>…o carga una lista (.txt)</span><input type="file" accept=".txt,text/plain" onchange={(e) => readFile(e, (t) => (proxies = t))} /></label>
    <div class="end"><button type="submit" class="btn btn-primary" disabled={!proxies.trim()}>Importar</button></div>
  </form>

  <form class="panel pad" onsubmit={addWG} aria-labelledby="net-wg">
    <h2 id="net-wg">Añadir túnel WireGuard</h2>
    <label class="field"><span>Nombre</span><input class="input" bind:value={wgName} placeholder="mullvad-se" /></label>
    <label class="field">
      <span>Archivo .conf</span>
      <input type="file" accept=".conf,text/plain" onchange={(e) => readFile(e, (t, n) => {
        wgConf = t;
        if (!wgName) wgName = n.replace(/\.conf$/i, '');
      })} />
    </label>
    <label class="field"><span>…o pega su contenido</span><textarea class="textarea" rows="4" bind:value={wgConf} placeholder="[Interface]&#10;PrivateKey = …"></textarea></label>
    <p class="hint">El túnel funciona dentro de la app, sin permisos de administrador. La clave privada se guarda cifrada.</p>
    <div class="end"><button type="submit" class="btn btn-primary" disabled={!wgConf.trim()}>Añadir</button></div>
  </form>
</div>

<section class="panel pad" aria-labelledby="net-ro">
  <h2 id="net-ro">Router y VPN del sistema</h2>
  <p class="hint">Por seguridad estos comandos solo se editan en <code class="mono">config.toml</code>; aquí se muestran como referencia.</p>
  <dl class="ro">
    <dt>Reconexión del router</dt>
    <dd>
      {#if router?.enabled}
        {router.method === 'script' ? 'Por script' : 'Por UPnP'}
        {#if router.script?.length}<code class="mono cmd">{router.script.join(' ')}</code>{/if}
      {:else}
        <span class="muted">Desactivada</span>
      {/if}
    </dd>
    <dt>VPN del sistema</dt>
    <dd>
      {#if vpns.length}
        <ul class="vpn">
          {#each vpns as v, i (i)}
            <li>
              <span class="name">{v.name}</span>
              {#if v.connect?.length}<span class="muted">conectar</span> <code class="mono cmd">{v.connect.join(' ')}</code>{/if}
              {#if v.rotate?.length}<span class="muted">rotar</span> <code class="mono cmd">{v.rotate.join(' ')}</code>{/if}
              {#if v.disconnect?.length}<span class="muted">desconectar</span> <code class="mono cmd">{v.disconnect.join(' ')}</code>{/if}
            </li>
          {/each}
        </ul>
      {:else}
        <span class="muted">Ninguna configurada</span>
      {/if}
    </dd>
  </dl>
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0; font-size: 18px; font-weight: 600; letter-spacing: -0.01em; }
  .head > .muted { margin-right: auto; }
  .rot { min-height: 36px; }
  h2 { margin: 0; font-size: 14px; font-weight: 600; }
  .hint { margin: 0; }
  .eg { min-width: 860px; }
  .who { display: flex; flex-direction: column; gap: 2px; }
  .small { font-size: 12px; }
  .g { margin-left: 6px; }
  .ex { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
  .acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
  .cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 16px; }
  .pad { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
  .end { display: flex; justify-content: flex-end; }
  input[type='file'] { min-height: 36px; color: var(--muted); }
  .ro { display: grid; grid-template-columns: max-content 1fr; gap: 10px 20px; margin: 0; }
  @media (max-width: 560px) { .ro { grid-template-columns: 1fr; } }
  .ro dt { color: var(--muted); }
  .ro dd { margin: 0; display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .vpn { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .vpn li { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .name { font-weight: 500; margin-right: 4px; }
  .cmd { padding: 1px 6px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg); font-size: 12px; }
</style>
