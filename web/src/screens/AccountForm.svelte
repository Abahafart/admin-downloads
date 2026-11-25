<script lang="ts">
  // New account: password (+2FA later), cookie import, Google OAuth or API key.
  import { app } from '../lib/store/app.svelte';
  import Icon from '../components/Icon.svelte';
  import { ACCOUNT_HOSTERS, KIND_NAMES, hosterName } from '../lib/store/labels';
  import type { AccountKind } from '../lib/api/types';
  let { hoster: initialHoster = 'mega', kind: initialKind, onclose }: { hoster?: string; kind?: AccountKind; onclose: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let hoster = $state(initialHoster);
  const kinds = $derived<AccountKind[]>(ACCOUNT_HOSTERS.find((h) => h.id === hoster)?.kinds ?? ['password']);
  // svelte-ignore state_referenced_locally
  let kind = $state<AccountKind>(initialKind ?? ACCOUNT_HOSTERS.find((h) => h.id === initialHoster)?.kinds[0] ?? 'password');
  let label = $state('');
  let username = $state('');
  let password = $state('');
  let apiKey = $state('');
  let cookies = $state('');
  let allowRotation = $state(false);
  let busy = $state(false);

  $effect(() => {
    if (!kinds.includes(kind)) kind = kinds[0];
  });

  async function loadFile(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (f) cookies = await f.text();
  }

  async function submit(e: Event) {
    e.preventDefault();
    if (app.vaultLocked) return;
    busy = true;
    const name = label.trim() || hosterName(hoster);
    let ok = false;
    if (kind === 'cookie') {
      const a = await app.importCookies(hoster, name, cookies);
      if (a && allowRotation) await app.updateAccount(a.id, { allow_rotation: true });
      ok = !!a;
    } else if (kind === 'oauth') {
      const a = await app.createAccount({ hoster, kind, label: name, allow_rotation: allowRotation });
      if (a) await app.googleOAuth(a.id);
      ok = !!a;
    } else {
      const a = await app.createAccount({
        hoster, kind, label: name, allow_rotation: allowRotation,
        username: kind === 'password' ? username.trim() : undefined,
        password: kind === 'password' ? password : undefined,
        api_key: kind === 'apikey' ? apiKey.trim() : undefined,
      });
      ok = !!a;
    }
    busy = false;
    if (ok) onclose();
  }

  const valid = $derived(
    kind === 'password' ? !!username.trim() && !!password : kind === 'apikey' ? !!apiKey.trim() : kind === 'cookie' ? !!cookies.trim() : true,
  );
</script>

<form class="panel form" onsubmit={submit} aria-labelledby="acc-new">
  <h2 id="acc-new">Añadir cuenta</h2>
  {#if app.vaultLocked}
    <p class="locked" id="acc-locked" role="status">
      <Icon name="lock" size={16} />
      <span>Las credenciales están bloqueadas. Desbloquéalas con la contraseña maestra (arriba) para poder guardar la cuenta; lo que escribas aquí no se pierde.</span>
    </p>
  {/if}
  <div class="grid">
    <label class="field">
      <span>Hoster</span>
      <select class="select" bind:value={hoster}>
        {#each ACCOUNT_HOSTERS as h (h.id)}<option value={h.id}>{hosterName(h.id)}</option>{/each}
      </select>
    </label>
    <label class="field">
      <span>Tipo de credencial</span>
      <select class="select" bind:value={kind}>
        {#each kinds as k (k)}<option value={k}>{KIND_NAMES[k]}</option>{/each}
      </select>
    </label>
    <label class="field">
      <span>Nombre para reconocerla</span>
      <input class="input" bind:value={label} placeholder={hosterName(hoster)} />
    </label>
  </div>

  {#if kind === 'password'}
    <div class="grid">
      <label class="field"><span>Usuario o correo</span><input class="input" autocomplete="username" bind:value={username} required /></label>
      <label class="field"><span>Contraseña</span><input class="input" type="password" autocomplete="current-password" bind:value={password} required /></label>
    </div>
    {#if hoster === 'mega'}<p class="hint">Si tienes la verificación en dos pasos activada, te pediremos el código después.</p>{/if}
  {:else if kind === 'apikey'}
    <label class="field"><span>Clave de API</span><input class="input mono" autocomplete="off" bind:value={apiKey} required /></label>
  {:else if kind === 'cookie'}
    <label class="field">
      <span>Cookies (cookies.txt de Netscape o JSON)</span>
      <textarea class="textarea" rows="5" bind:value={cookies} placeholder="# Netscape HTTP Cookie File&#10;.terabox.com	TRUE	/	TRUE	…	ndus	…"></textarea>
    </label>
    <label class="field"><span>…o carga el archivo</span><input type="file" accept=".txt,.json,text/plain,application/json" onchange={loadFile} /></label>
    <p class="hint">La extensión del navegador puede exportarlas por ti. Las cookies se guardan cifradas y nunca aparecen en los registros.</p>
  {:else if kind === 'oauth'}
    <p class="hint">Se abrirá la página de Google para dar permiso de lectura. Con una cuenta conectada, los archivos con la cuota agotada se copian a tu unidad y se descarga la copia.</p>
  {/if}

  <label class="check"><input type="checkbox" bind:checked={allowRotation} /> Permitir rotar la IP con esta cuenta</label>
  <p class="hint">Desactivado por defecto: la cuota de una cuenta no depende de la IP y algunos hosters bloquean las cuentas que cambian de IP.</p>

  <div class="actions">
    <button type="button" class="btn" onclick={onclose}>Cancelar</button>
    <button type="submit" class="btn btn-primary" disabled={!valid || busy || app.vaultLocked} aria-describedby={app.vaultLocked ? 'acc-locked' : undefined}>
      {kind === 'oauth' ? 'Conectar con Google' : kind === 'cookie' ? 'Importar cookies' : 'Añadir cuenta'}
    </button>
  </div>
</form>

<style>
  .form { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
  h2 { margin: 0; font-size: 14px; font-weight: 600; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
  .actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; }
  input[type='file'] { min-height: 36px; color: var(--muted); }
  .locked { margin: 0; display: flex; align-items: flex-start; gap: 8px; color: var(--warn); }
  .locked span { color: var(--text); }
</style>
