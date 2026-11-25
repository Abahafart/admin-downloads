<script lang="ts">
  // Ajustes › Credenciales: where account secrets live (GET /secrets), lock
  // the vault and change its master password (POST /secrets/lock and
  // /secrets/password). Unlocking is done from the vault banner.
  import { app } from '../lib/store/app.svelte';
  import { MIN_MASTER_PASSWORD } from '../lib/api/types';

  const st = $derived(app.secrets);
  const vault = $derived(!!st?.lockable);

  const BACKENDS: Record<string, string> = {
    keyring: 'En el llavero del sistema',
    vault: 'En un archivo cifrado con una contraseña maestra',
    memory: 'Solo en memoria: se pierden al reiniciar el servicio',
  };
  const where = $derived(st ? (BACKENDS[st.backend] ?? st.backend) : '');
  const stateLabel = $derived(!st || !vault ? '' : !st.initialized ? 'Sin contraseña maestra todavía' : st.locked ? 'Bloqueadas' : 'Desbloqueadas');

  let locking = $state(false);
  async function lock() {
    locking = true;
    await app.lockSecrets();
    locking = false;
  }

  let oldPw = $state('');
  let newPw = $state('');
  let repeat = $state('');
  let error = $state('');
  let busy = $state(false);
  let blockedUntil = $state(0);
  const wait = $derived(Math.max(0, Math.ceil((blockedUntil - app.now) / 1000)));
  const short = $derived(newPw.length > 0 && newPw.length < MIN_MASTER_PASSWORD);
  const mismatch = $derived(repeat.length > 0 && repeat !== newPw);
  const same = $derived(!!newPw && newPw === oldPw);
  const valid = $derived(!!oldPw && newPw.length >= MIN_MASTER_PASSWORD && repeat === newPw && !same);
  const problem = $derived(
    short ? `La nueva contraseña necesita al menos ${MIN_MASTER_PASSWORD} caracteres.` : mismatch ? 'Las dos contraseñas nuevas no coinciden.' : same ? 'La nueva contraseña es igual a la actual.' : '',
  );

  async function change(e: Event) {
    e.preventDefault();
    if (!valid || busy || wait > 0) return;
    busy = true;
    error = '';
    const r = await app.changeMasterPassword(oldPw, newPw);
    busy = false;
    if (r.ok) {
      oldPw = newPw = repeat = '';
      return;
    }
    if (r.status === 429) blockedUntil = Date.now() + (r.retryAfter ?? 30) * 1000;
    error = r.status === 429 ? 'Demasiados intentos fallidos.' : r.status === 403 ? 'La contraseña maestra actual no es correcta.' : r.message;
    if (r.status === 403) oldPw = '';
  }
</script>

{#if st}
  <section class="panel pad" aria-labelledby="aj-cred">
    <h2 id="aj-cred">Credenciales de las cuentas</h2>
    <dl class="facts">
      <div><dt>Dónde se guardan</dt><dd>{where}</dd></div>
      {#if stateLabel}<div><dt>Estado</dt><dd class:tone-warn={st.locked}>{stateLabel}</dd></div>{/if}
    </dl>
    {#if st.warning}<p class="hint tone-warn">{st.warning}</p>{/if}

    {#if !vault}
      <p class="hint">
        {st.backend === 'keyring'
          ? 'El sistema protege las credenciales con tu sesión: no hace falta contraseña maestra.'
          : 'Sin llavero ni archivo cifrado no hay nada que bloquear.'}
      </p>
    {:else if !st.initialized}
      <p class="hint">Elige la contraseña maestra en <a href="#/cuentas">Cuentas</a> al añadir la primera cuenta.</p>
    {:else}
      {#if st.locked}
        <p class="hint">Desbloquéalas con la contraseña maestra en <a href="#/cuentas">Cuentas</a> para poder usar las cuentas.</p>
      {:else}
        <div class="row">
          <button type="button" class="btn" disabled={locking} onclick={lock}>{locking ? 'Bloqueando…' : 'Bloquear ahora'}</button>
          <p class="hint">Hasta que vuelvas a escribir la contraseña maestra no se podrán usar ni guardar cuentas.</p>
        </div>
      {/if}

      <form class="form" onsubmit={change} aria-labelledby="aj-cred-pw">
        <h3 id="aj-cred-pw">Cambiar la contraseña maestra</h3>
        <input class="sr-only" type="text" name="username" autocomplete="username" value="adesc-vault" readonly tabindex="-1" aria-hidden="true" />
        <div class="grid">
          <label class="field"><span>Contraseña actual</span><input class="input" type="password" autocomplete="current-password" bind:value={oldPw} required /></label>
          <label class="field">
            <span>Nueva (mínimo {MIN_MASTER_PASSWORD} caracteres)</span>
            <input class="input" type="password" autocomplete="new-password" minlength={MIN_MASTER_PASSWORD} aria-invalid={short || same || undefined} bind:value={newPw} required />
          </label>
          <label class="field">
            <span>Repite la nueva</span>
            <input class="input" type="password" autocomplete="new-password" aria-invalid={mismatch || undefined} bind:value={repeat} required />
          </label>
        </div>
        {#if problem}<p class="hint tone-err" aria-live="polite">{problem}</p>{/if}
        {#if error}<p class="error-text" role="alert">{error}{#if wait > 0}{` Vuelve a intentarlo en ${wait} s.`}{/if}</p>{/if}
        <div class="end">
          <button type="submit" class="btn btn-primary" disabled={!valid || busy || wait > 0}>
            {busy ? 'Cambiando…' : wait > 0 ? `Espera ${wait} s` : 'Cambiar contraseña'}
          </button>
        </div>
      </form>
    {/if}
  </section>
{/if}

<style>
  h2 { margin: 0; font-size: 14px; font-weight: 600; }
  h3 { margin: 0; font-size: 13px; font-weight: 600; }
  .pad { padding: 16px; display: flex; flex-direction: column; gap: 14px; }
  .facts { margin: 0; display: flex; flex-wrap: wrap; gap: 8px 32px; }
  .facts div { display: flex; flex-direction: column; gap: 2px; }
  dt { color: var(--muted); font-size: 0.92em; }
  dd { margin: 0; }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
  .end { display: flex; justify-content: flex-end; }
  /* .hint (base.css) comes after the tone classes: keep the tone. */
  .hint.tone-warn { color: var(--warn); }
  .hint.tone-err { color: var(--err); }
</style>
