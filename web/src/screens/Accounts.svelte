<script lang="ts">
  // Cuentas (shared, Grafito language): one table per hoster with status,
  // premium, quota and the AllowRotation toggle; password + 2FA, cookie
  // import, Google OAuth and API keys.
  import AccountForm from './AccountForm.svelte';
  import Icon from '../components/Icon.svelte';
  import VaultBanner from '../components/VaultBanner.svelte';
  import OAuthPaste from '../components/OAuthPaste.svelte';
  import ProgressBar from '../components/ProgressBar.svelte';
  import { app } from '../lib/store/app.svelte';
  import { router } from '../lib/router.svelte';
  import { formatAgo, formatBytes, formatDate, percent } from '../lib/store/format';
  import { ACCOUNT_STATUS, KIND_NAMES, hosterName } from '../lib/store/labels';
  import type { Account, AccountKind } from '../lib/api/types';

  let form = $state<{ hoster: string; kind?: AccountKind } | null>(null);
  let codes = $state<Record<string, string>>({});
  let arm = $state<string | null>(null);

  $effect(() => {
    const alta = router.params.get('alta');
    if (alta) form = { hoster: alta, kind: alta === 'gdrive' ? 'oauth' : undefined };
    if (router.params.get('oauth')) {
      app.cancelOAuth();
      app.toast('Cuenta de Google conectada', 'success');
      void app.loadAccounts();
      router.go('cuentas');
    }
  });

  const groups = $derived.by(() => {
    const m = new Map<string, Account[]>();
    for (const a of app.accounts) {
      const l = m.get(a.hoster);
      if (l) l.push(a);
      else m.set(a.hoster, [a]);
    }
    return [...m.entries()].sort((a, b) => hosterName(a[0]).localeCompare(hosterName(b[0])));
  });

  function tone(a: Account): string {
    switch (a.status) {
      case 'ok':
        return 'tone-text';
      case 'invalid':
      case 'expired':
      case 'error':
        return 'tone-err';
      case 'quota':
      case 'needs_2fa':
        return 'tone-warn';
      default:
        return 'muted';
    }
  }

  function del(a: Account) {
    if (arm === a.id) {
      arm = null;
      void app.deleteAccount(a.id);
    } else {
      arm = a.id;
      setTimeout(() => arm === a.id && (arm = null), 4000);
    }
  }

  async function send2fa(e: Event, a: Account) {
    e.preventDefault();
    await app.submit2FA(a.id, (codes[a.id] ?? '').trim());
    codes[a.id] = '';
  }
</script>

<header class="head">
  <h1>Cuentas</h1>
  <button type="button" class="btn btn-primary add" aria-expanded={!!form} onclick={() => (form = form ? null : { hoster: 'mega' })}>
    <Icon name="plus" /> Añadir cuenta
  </button>
</header>

<VaultBanner context="cuentas" />
<OAuthPaste />

{#if form}
  {#key form}
    <AccountForm hoster={form.hoster} kind={form.kind} onclose={() => (form = null)} />
  {/key}
{/if}

{#if !groups.length}
  <p class="muted">Todavía no hay cuentas. Con una cuenta se descarga más rápido y sin las esperas de las descargas anónimas.</p>
{/if}

{#each groups as [hoster, list] (hoster)}
  <section aria-labelledby="acc-{hoster}" class="group">
    <h2 id="acc-{hoster}">{hosterName(hoster)} <span class="muted">{list.length}</span></h2>
    <div class="table-wrap">
      <table class="table accts">
        <colgroup><col class="c-who" /><col class="c-st" /><col class="c-pr" /><col class="c-q" /><col class="c-rot" /><col /></colgroup>
        <thead>
          <tr>
            <th scope="col">Cuenta</th>
            <th scope="col">Estado</th>
            <th scope="col">Premium</th>
            <th scope="col">Cuota</th>
            <th scope="col">Rotar IP</th>
            <th scope="col"><span class="sr-only">Acciones</span></th>
          </tr>
        </thead>
        <tbody>
          {#each list as a (a.id)}
            <tr class:disabled={!a.enabled}>
              <td>
                <div class="who">
                  <span class="lbl">{a.label}</span>
                  <span class="muted">{a.username || KIND_NAMES[a.kind]}</span>
                </div>
              </td>
              <td>
                <div class="st">
                  <span class={tone(a)}>{a.enabled ? ACCOUNT_STATUS[a.status] : 'Desactivada'}</span>
                  {#if a.status_msg}<span class="muted msg">{a.status_msg}</span>{/if}
                  <span class="muted small">Comprobada {formatAgo(a.checked_at, app.now)}</span>
                  {#if a.status === 'needs_2fa'}
                    <form class="tfa" onsubmit={(e) => send2fa(e, a)}>
                      <label class="sr-only" for="tfa-{a.id}">Código de verificación de {a.label}</label>
                      <input id="tfa-{a.id}" class="input mono" inputmode="numeric" autocomplete="one-time-code" maxlength="8" placeholder="Código 2FA" bind:value={codes[a.id]} />
                      <button type="submit" class="btn btn-sm" disabled={!(codes[a.id] ?? '').trim()}>Enviar</button>
                    </form>
                  {/if}
                  {#if a.status === 'expired' && a.kind === 'cookie'}
                    <button type="button" class="btn btn-sm" onclick={() => (form = { hoster: a.hoster, kind: 'cookie' })}>Importar cookies nuevas</button>
                  {/if}
                  {#if (a.status === 'expired' || a.status === 'invalid' || a.status === 'unknown') && a.kind === 'oauth'}
                    <button type="button" class="btn btn-sm" onclick={() => app.googleOAuth(a.id)}>Conectar con Google</button>
                  {/if}
                </div>
              </td>
              <td>{a.premium ? (a.premium_until ? `Sí, hasta ${formatDate(a.premium_until)}` : 'Sí') : 'No'}</td>
              <td class="quota">
                {#if a.quota_total > 0 && a.quota_used >= 0}
                  <span class="tabular">{formatBytes(a.quota_used)} de {formatBytes(a.quota_total)}</span>
                  <ProgressBar value={percent(a.quota_used, a.quota_total)} tone={percent(a.quota_used, a.quota_total) >= 90 ? 'warn' : 'accent'} label="Cuota usada de {a.label}" />
                {:else}
                  <span class="muted">—</span>
                {/if}
              </td>
              <td>
                <label class="check">
                  <input type="checkbox" checked={a.allow_rotation} onchange={(e) => app.updateAccount(a.id, { allow_rotation: (e.currentTarget as HTMLInputElement).checked })} />
                  <span>{a.allow_rotation ? 'Sí' : 'No'}</span>
                  <span class="sr-only">permitir rotar la IP con {a.label}</span>
                </label>
              </td>
              <td>
                <div class="acts">
                  <button type="button" class="btn btn-sm" disabled={a.status === 'checking'} onclick={() => app.checkAccount(a.id)}>{a.status === 'checking' ? 'Comprobando…' : 'Comprobar'}</button>
                  <button type="button" class="btn btn-sm" onclick={() => app.updateAccount(a.id, { enabled: !a.enabled })}>{a.enabled ? 'Desactivar' : 'Activar'}</button>
                  <button type="button" class="btn btn-sm btn-danger" class:btn-icon={arm !== a.id} aria-label={arm === a.id ? `Confirmar eliminar ${a.label}` : `Eliminar ${a.label}`} onclick={() => del(a)}>
                    {#if arm === a.id}¿Eliminar?{:else}<Icon name="trash" size={14} />{/if}
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/each}

<p class="hint">Rotar la IP con una cuenta está desactivado por defecto: la cuota de una cuenta no depende de la IP y cambiarla puede hacer que el hoster la bloquee.</p>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  h1 { margin: 0 auto 0 0; font-size: 18px; font-weight: 600; letter-spacing: -0.01em; }
  .add { min-height: 36px; }
  .group { display: flex; flex-direction: column; gap: 8px; }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .accts { min-width: 900px; table-layout: fixed; }
  .c-who { width: 20%; }
  .c-st { width: 25%; }
  .c-pr { width: 12%; }
  .c-q { width: 15%; }
  .c-rot { width: 9%; }
  .accts td { overflow-wrap: anywhere; }
  .who, .st { display: flex; flex-direction: column; gap: 2px; }
  .lbl { font-weight: 500; }
  .msg { max-width: 280px; }
  .small { font-size: 12px; }
  .quota { min-width: 160px; }
  .quota span { display: block; margin-bottom: 6px; }
  .acts { display: flex; flex-wrap: wrap; gap: 6px; justify-content: flex-end; }
  .tfa { display: flex; gap: 6px; margin-top: 4px; }
  .tfa .input { width: 120px; }
  tr.disabled .lbl { color: var(--muted); }
</style>
