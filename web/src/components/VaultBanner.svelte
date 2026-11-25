<script lang="ts">
  // Locked secret vault: a banner with the unlock form (PLAN.md section 5).
  // `global` is rendered by App above every screen except Cuentas, and only
  // for an existing vault or when accounts exist (nothing to nag about
  // before the first account);
  // `cuentas` sits on top of Cuentas and also explains how the first unlock
  // creates the vault and shows the store warning.
  import Icon from './Icon.svelte';
  import { app } from '../lib/store/app.svelte';
  import { MIN_MASTER_PASSWORD } from '../lib/api/types';

  let { context = 'global' }: { context?: 'global' | 'cuentas' } = $props();

  const st = $derived(app.secrets);
  const creating = $derived(!!st && !st.initialized);
  let hidden = $state(false);
  const show = $derived(!!st?.locked && (context === 'cuentas' || ((st.initialized || app.accounts.length > 0) && !hidden)));
  const warning = $derived(context === 'cuentas' ? (st?.warning ?? '') : '');

  let password = $state('');
  let repeat = $state('');
  let error = $state('');
  let busy = $state(false);
  let blockedUntil = $state(0);
  const wait = $derived(Math.max(0, Math.ceil((blockedUntil - app.now) / 1000)));
  const uid = $props.id();

  const short = $derived(creating && password.length > 0 && password.length < MIN_MASTER_PASSWORD);
  const mismatch = $derived(creating && repeat.length > 0 && repeat !== password);
  const valid = $derived(!!password && (!creating || (password.length >= MIN_MASTER_PASSWORD && repeat === password)));

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || busy || wait > 0) return;
    busy = true;
    error = '';
    const r = await app.unlockSecrets(password);
    busy = false;
    if (r.ok) {
      password = '';
      repeat = '';
      return;
    }
    if (r.status === 429) blockedUntil = Date.now() + (r.retryAfter ?? 30) * 1000;
    error = r.status === 429 ? 'Demasiados intentos fallidos.' : r.message;
    if (r.status === 403) password = '';
  }
</script>

{#if show}
  <section class="panel vault" aria-labelledby="{uid}-t">
    <div class="msg">
      <span class="ic"><Icon name="lock" size={18} /></span>
      <div class="txt">
        <p class="title" id="{uid}-t">{creating ? 'Elige la contraseña maestra de las credenciales' : 'Credenciales bloqueadas'}</p>
        {#if creating}
          <p class="hint">
            Este equipo no tiene llavero del sistema, así que las contraseñas, cookies y claves de las cuentas se guardan en un
            archivo cifrado. Elige una contraseña maestra de al menos {MIN_MASTER_PASSWORD} caracteres: la primera vez crea el archivo
            y después se pide cada vez que arranca el servicio. Si la olvidas, habrá que volver a añadir las cuentas.
          </p>
        {:else}
          <p class="hint">
            Las credenciales de las cuentas están en un archivo cifrado. Escribe la contraseña maestra para usarlas: mientras
            tanto no se pueden usar ni guardar cuentas.
          </p>
        {/if}
        {#if warning}<p class="hint tone-warn">{warning}</p>{/if}
      </div>
      {#if context === 'global'}
        <button type="button" class="btn btn-ghost btn-sm" onclick={() => (hidden = true)}>Ahora no</button>
      {/if}
    </div>
    <form class="form" onsubmit={submit} aria-describedby={error ? `${uid}-err` : undefined}>
      <!-- Lets password managers file the master password under its own name. -->
      <input class="sr-only" type="text" name="username" autocomplete="username" value="adesc-vault" readonly tabindex="-1" aria-hidden="true" />
      <label class="field">
        <span>{creating ? 'Nueva contraseña maestra' : 'Contraseña maestra'}</span>
        <input
          class="input"
          type="password"
          name="master"
          autocomplete={creating ? 'new-password' : 'current-password'}
          minlength={creating ? MIN_MASTER_PASSWORD : undefined}
          aria-invalid={short || undefined}
          aria-describedby={creating ? `${uid}-len` : undefined}
          bind:value={password}
          required
        />
      </label>
      {#if creating}
        <label class="field">
          <span>Repítela</span>
          <input class="input" type="password" name="repeat" autocomplete="new-password" aria-invalid={mismatch || undefined} bind:value={repeat} required />
        </label>
      {/if}
      <button type="submit" class="btn btn-primary" disabled={!valid || busy || wait > 0}>
        {busy ? 'Comprobando…' : wait > 0 ? `Espera ${wait} s` : creating ? 'Crear y desbloquear' : 'Desbloquear'}
      </button>
    </form>
    {#if creating}
      <p class="hint" id="{uid}-len" class:tone-err={short}>
        {short ? `Faltan ${MIN_MASTER_PASSWORD - password.length} caracteres (mínimo ${MIN_MASTER_PASSWORD}).` : mismatch ? 'Las dos contraseñas no coinciden.' : `Mínimo ${MIN_MASTER_PASSWORD} caracteres.`}
      </p>
    {/if}
    {#if error}
      <p class="error-text" id="{uid}-err" role="alert">
        {error}{#if wait > 0}{` Vuelve a intentarlo en ${wait} s.`}{/if}
      </p>
    {/if}
  </section>
{/if}

<style>
  .vault { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; border-color: var(--warn); }
  .msg { display: flex; align-items: flex-start; gap: 10px; }
  .ic { color: var(--warn); padding-top: 2px; display: inline-flex; }
  .txt { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .title { margin: 0; font-weight: 600; }
  .form { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 8px 12px; }
  .form .field { flex: 1 1 200px; max-width: 320px; }
  .form .btn-primary { flex: none; }
  /* .hint (base.css) comes after the tone classes: keep the tone. */
  .hint.tone-warn { color: var(--warn); }
  .hint.tone-err { color: var(--err); }
</style>
