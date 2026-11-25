<script lang="ts">
  // ctrl+k command bar: /pausa, /rotar, /reintentar fallidas… or paste links.
  import Modal from './Modal.svelte';
  import { app } from '../lib/store/app.svelte';
  import { hasLinks, runLine, suggest } from '../lib/commands';
  let value = $state('');
  let active = $state(0);
  let input: HTMLInputElement | undefined = $state();
  const list = $derived(suggest(value));

  $effect(() => {
    if (app.ui.commandOpen) {
      value = '';
      active = 0;
      queueMicrotask(() => input?.focus());
    }
  });

  function close() {
    app.ui.commandOpen = false;
  }

  async function exec(line: string) {
    close();
    await runLine(app, line);
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      active = Math.min(list.length - 1, active + 1);
      e.preventDefault();
    } else if (e.key === 'ArrowUp') {
      active = Math.max(0, active - 1);
      e.preventDefault();
    } else if (e.key === 'Tab' && list[active]) {
      value = list[active].name + (list[active].arg ? ' ' : '');
      e.preventDefault();
    }
  }

  function submit(e: Event) {
    e.preventDefault();
    const v = value.trim();
    if (!v && list[active]) return exec(list[active].name);
    if (hasLinks(v) || !list.length || v.includes(' ')) return exec(v);
    const exact = list.find((c) => c.name === v.toLowerCase());
    return exec(exact ? exact.name : list[active]?.name ?? v);
  }
</script>

<Modal open={app.ui.commandOpen} title="Barra de comandos" onclose={close} width={600}>
  <form onsubmit={submit} class="cmd">
    <label for="cmd-input" class="sr-only">Comando o enlaces</label>
    <input
      id="cmd-input"
      bind:this={input}
      bind:value
      class="input mono"
      autocomplete="off"
      spellcheck="false"
      placeholder="/pausa, /rotar, /reintentar fallidas… o pega enlaces"
      role="combobox"
      aria-expanded={list.length > 0}
      aria-controls="cmd-list"
      aria-activedescendant={list[active] ? `cmd-${active}` : undefined}
      onkeydown={keydown}
      oninput={() => (active = 0)}
    />
    {#if hasLinks(value)}
      <p class="hint">Intro añade los enlaces al LinkGrabber.</p>
    {/if}
    {#if list.length}
      <ul id="cmd-list" role="listbox" aria-label="Comandos">
        {#each list as c, i (c.name)}
          <li id="cmd-{i}" role="option" aria-selected={i === active}>
            <button type="button" class="opt" class:on={i === active} tabindex="-1" onclick={() => exec(c.arg ? c.name + ' ' + value.split(' ').slice(1).join(' ') : c.name)}>
              <span class="mono">{c.name}{#if c.arg}<span class="muted"> {c.arg}</span>{/if}</span>
              <span class="muted">{c.label}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </form>
</Modal>

<style>
  .cmd { display: flex; flex-direction: column; gap: 10px; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; max-height: 50vh; overflow: auto; }
  .opt {
    width: 100%; min-height: 36px; display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 12px; align-items: center;
    padding: 6px 10px; border: 0; border-radius: var(--radius-md); background: transparent; color: var(--text); cursor: pointer; text-align: left;
  }
  .opt.on, .opt:hover { background: var(--raised); }
</style>
