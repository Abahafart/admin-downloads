<script lang="ts">
  // Google OAuth in progress: when the daemon runs on another machine the
  // browser cannot reach its callback after consent, so the user pastes the
  // final address here (POST /api/v1/oauth/google/complete {url}).
  import Icon from './Icon.svelte';
  import { app } from '../lib/store/app.svelte';

  const pending = $derived(app.oauth);
  let url = $state('');
  let error = $state('');
  let busy = $state(false);
  const uid = $props.id();

  const looksRight = $derived(/[?&#](code|error)=/.test(url) && /[?&#]state=/.test(url));
  const showFormat = $derived(!!url.trim() && !looksRight);

  async function submit(e: Event) {
    e.preventDefault();
    if (!looksRight || busy) return;
    busy = true;
    error = '';
    const r = await app.completeGoogleOAuth(url);
    busy = false;
    if (r.ok) url = '';
    else error = r.message || 'No se pudo conectar la cuenta de Google.';
  }

  function again() {
    error = '';
    if (pending) void app.googleOAuth(pending.accountId);
  }
</script>

{#if pending}
  <section class="panel oauth" aria-labelledby="{uid}-t">
    <div class="msg">
      <span class="ic"><Icon name="link" size={18} /></span>
      <div class="txt">
        <p class="title" id="{uid}-t">Conectando «{pending.label}» con Google</p>
        {#if pending.blocked}
          <p class="hint tone-warn">El navegador bloqueó la ventana: ábrela con «Abrir la página de Google».</p>
        {/if}
        <p class="hint">
          Da permiso en la página de Google. Si al terminar la página no carga (el servicio de descargas está en otro equipo),
          copia la dirección completa de esa página desde la barra del navegador y pégala aquí.
        </p>
      </div>
    </div>
    <form class="form" onsubmit={submit}>
      <label class="field grow">
        <span>Pega aquí la URL final</span>
        <input
          class="input mono"
          type="text"
          inputmode="url"
          autocomplete="off"
          spellcheck="false"
          placeholder="http://…/api/v1/oauth/google/callback?state=…&code=…"
          aria-invalid={showFormat || undefined}
          aria-describedby="{uid}-fmt"
          bind:value={url}
        />
      </label>
      <button type="submit" class="btn btn-primary" disabled={!looksRight || busy}>{busy ? 'Conectando…' : 'Conectar'}</button>
    </form>
    <p class="hint" id="{uid}-fmt" class:tone-err={showFormat}>
      {showFormat ? 'Esa dirección no parece la de vuelta de Google: debe incluir «state=» y «code=».' : 'La dirección contiene un código de un solo uso; no la compartas.'}
    </p>
    {#if error}<p class="error-text" role="alert">{error}</p>{/if}
    <div class="acts">
      <a class="btn btn-sm" href={pending.url} target="_blank" rel="noopener noreferrer">Abrir la página de Google</a>
      <button type="button" class="btn btn-sm" onclick={again}>Pedir un enlace nuevo</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick={() => app.cancelOAuth()}>Cerrar</button>
    </div>
  </section>
{/if}

<style>
  .oauth { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; }
  .msg { display: flex; align-items: flex-start; gap: 10px; }
  .ic { color: var(--accent); padding-top: 2px; display: inline-flex; }
  .txt { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .title { margin: 0; font-weight: 600; }
  .form { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 8px 12px; }
  .grow { flex: 1 1 260px; }
  .acts { display: flex; flex-wrap: wrap; gap: 8px; }
  /* .hint (base.css) comes after the tone classes: keep the tone. */
  .hint.tone-warn { color: var(--warn); }
  .hint.tone-err { color: var(--err); }
</style>
