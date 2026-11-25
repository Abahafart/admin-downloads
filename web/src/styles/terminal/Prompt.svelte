<script lang="ts">
  // B · Terminal "›" prompt (docs/design/Terminal.dc.html): one field for
  // everything. Pasted links go to the LinkGrabber, "/…" runs a command
  // (COMMANDS / suggest / runLine, the same as the ctrl+k bar) and plain text
  // filters the queue (it is the "/" search field).
  import { app } from '../../lib/store/app.svelte';
  import { router } from '../../lib/router.svelte';
  import { hasLinks, runLine, suggest } from '../../lib/commands';
  import { promptState } from './prompt.svelte';

  let value = $state(app.ui.search);
  let active = $state(0);
  let focused = $state(false);
  let input: HTMLInputElement | undefined = $state();
  const isCmd = $derived(value.trimStart().startsWith('/'));
  const links = $derived(hasLinks(value));
  const list = $derived(isCmd && !links ? suggest(value.trimStart()) : []);
  const showList = $derived(focused && list.length > 0);

  $effect(() => {
    promptState.mounted = true;
    return () => {
      promptState.mounted = false;
    };
  });

  let seen = promptState.focusRequests;
  $effect(() => {
    const n = promptState.focusRequests;
    if (n === seen) return;
    seen = n;
    if (!value.trim()) value = '/';
    active = 0;
    input?.focus();
  });

  // Plain text is the search; commands and links never filter the queue.
  $effect(() => {
    const q = isCmd || links ? '' : value;
    if (app.ui.search !== q) app.ui.search = q;
  });

  function clear() {
    value = '';
    active = 0;
  }

  async function exec(line: string) {
    clear();
    await runLine(app, line);
  }

  function submit(e: Event) {
    e.preventDefault();
    const v = value.trim();
    if (!v) return;
    if (links) return exec(v);
    if (isCmd) {
      if (!list.length || v.includes(' ')) return exec(v);
      const exact = list.find((c) => c.name === v.toLowerCase());
      return exec(exact ? exact.name : (list[active]?.name ?? v));
    }
    if (router.screen !== 'descargas') router.go('descargas');
  }

  function pick(name: string, needsArg: boolean) {
    if (!needsArg) return exec(name);
    value = name + ' ';
    input?.focus();
  }

  function keydown(e: KeyboardEvent) {
    if (showList && e.key === 'ArrowDown') {
      active = Math.min(list.length - 1, active + 1);
      e.preventDefault();
    } else if (showList && e.key === 'ArrowUp') {
      active = Math.max(0, active - 1);
      e.preventDefault();
    } else if (showList && e.key === 'Tab' && list[active] && !e.shiftKey) {
      value = list[active].name + (list[active].arg ? ' ' : '');
      e.preventDefault();
    } else if (e.key === 'Escape' && value) {
      clear();
    }
  }

  function paste(e: ClipboardEvent) {
    const t = e.clipboardData?.getData('text') ?? '';
    if (!value.trim() && hasLinks(t)) {
      e.preventDefault();
      void app.addLinks(t);
    }
  }
</script>

<form class="prompt" onsubmit={submit}>
  <label class="box" class:on={focused}>
    <span class="gt" aria-hidden="true">›</span>
    <span class="sr-only">Pegar enlaces, buscar o escribir un comando</span>
    <input
      id="t-prompt"
      bind:this={input}
      bind:value
      type="text"
      autocomplete="off"
      spellcheck="false"
      placeholder="pega enlaces · /pausa · /rotar · /reintentar fallidas"
      data-shortcut="search"
      aria-keyshortcuts="/ Control+K"
      role="combobox"
      aria-expanded={showList}
      aria-controls="t-cmds"
      aria-autocomplete="list"
      aria-activedescendant={showList && list[active] ? `t-cmd-${active}` : undefined}
      onkeydown={keydown}
      oninput={() => (active = 0)}
      onfocus={() => (focused = true)}
      onblur={() => setTimeout(() => (focused = false), 120)}
      onpaste={paste}
    />
    <span class="hint" aria-hidden="true">{links ? 'intro añade' : value && !isCmd ? 'buscando' : 'ctrl+k'}</span>
  </label>
  {#if showList}
    <ul id="t-cmds" role="listbox" aria-label="Comandos">
      {#each list as c, i (c.name)}
        <li id="t-cmd-{i}" role="option" aria-selected={i === active}>
          <button type="button" tabindex="-1" class:on={i === active} onmousedown={(e) => e.preventDefault()} onclick={() => pick(c.name, !!c.arg)}>
            <span class="nm">{c.name}{#if c.arg}<span class="muted"> {c.arg}</span>{/if}</span>
            <span class="muted">{c.label.toLowerCase()}</span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if links}<p class="sr-only" aria-live="polite">Intro añade los enlaces al LinkGrabber</p>{/if}
</form>

<style>
  .prompt { position: relative; }
  .box {
    display: flex; align-items: center; gap: 12px; min-height: 48px; padding: 0 16px;
    border: 1px solid var(--border-strong); border-radius: var(--radius-sm); background: var(--surface); cursor: text;
  }
  .box.on { border-color: var(--accent); }
  .box:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .gt { color: var(--accent); font-weight: 700; }
  input { flex: 1; min-width: 0; min-height: 44px; border: 0; outline: 0; background: transparent; color: var(--text); padding: 0; }
  input::placeholder { color: var(--muted); opacity: 1; }
  .hint { color: var(--muted); white-space: nowrap; }
  ul {
    position: absolute; left: 0; right: 0; top: calc(100% + 4px); z-index: 30; list-style: none; margin: 0; padding: 4px 0;
    border: 1px solid var(--border-strong); background: var(--surface); max-height: 50vh; overflow: auto;
  }
  li button {
    width: 100%; min-height: 36px; display: flex; flex-wrap: wrap; justify-content: space-between; gap: 2px 16px; align-items: center;
    padding: 4px 16px; border: 0; background: transparent; color: var(--text); text-align: left; cursor: pointer;
  }
  li button.on, li button:hover { background: var(--raised); }
  li button.on .nm::before { content: '› '; color: var(--accent); }
  @media (max-width: 560px) {
    .hint { display: none; }
    .box { padding: 0 12px; }
  }
</style>
