<script lang="ts">
  import { app } from '../lib/store/app.svelte';
  import Icon from './Icon.svelte';
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      await app.login(password);
      password = '';
    } catch (err) {
      error = err instanceof Error ? err.message : 'No se pudo iniciar sesión';
    } finally {
      busy = false;
    }
  }
</script>

<main class="login">
  <form class="panel card" onsubmit={submit}>
    <div class="brand"><Icon name="download" size={20} stroke={1.8} /><span>adesc</span></div>
    <h1>Iniciar sesión</h1>
    <p class="hint">Esta Web UI está protegida con contraseña (se define con <code class="mono">adesc password</code> o en config.toml).</p>
    <label class="field">
      <span>Contraseña</span>
      <!-- svelte-ignore a11y_autofocus -->
      <input class="input" type="password" autocomplete="current-password" bind:value={password} required autofocus />
    </label>
    {#if error}<p class="error-text" role="alert">{error}</p>{/if}
    <button type="submit" class="btn btn-primary" disabled={busy || !password}>{busy ? 'Entrando…' : 'Entrar'}</button>
  </form>
</main>

<style>
  .login { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
  .card { width: min(380px, 100%); padding: 24px; display: flex; flex-direction: column; gap: 14px; }
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 600; }
  h1 { margin: 0; font-size: 18px; font-weight: 600; }
</style>
