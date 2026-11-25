// Global keyboard shortcuts (PLAN.md 7.1), identical in the 5 styles. They
// act on the selected row and are ignored while typing in a field.
// Row navigation is DOM based: every style marks its selectable rows with
// data-row-key="<key>" (keys from lib/store/rows.ts), in visual order, and
// its search field with data-shortcut="search".
import type { AppStore } from './store/app.svelte';
import { parseKey } from './store/rows';

export interface Shortcut {
  keys: string[];
  label: string;
}

export const SHORTCUTS: Shortcut[] = [
  { keys: ['p'], label: 'Pausar o reanudar' },
  { keys: ['r'], label: 'Reintentar' },
  { keys: ['R'], label: 'Reiniciar desde cero' },
  { keys: ['i'], label: 'Rotar la IP' },
  { keys: ['/'], label: 'Buscar' },
  { keys: ['Ctrl', 'K'], label: 'Barra de comandos' },
  { keys: ['?'], label: 'Esta ayuda' },
  { keys: ['↑', '↓'], label: 'Elegir descarga (también j y k)' },
  { keys: ['→', '←'], label: 'Desplegar o plegar carpeta' },
  { keys: ['Supr'], label: 'Eliminar la seleccionada' },
  { keys: ['Esc'], label: 'Cerrar o quitar la selección' },
];

export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || !el.tagName) return false;
  const tag = el.tagName.toLowerCase();
  if (tag === 'textarea' || tag === 'select' || el.isContentEditable) return true;
  if (tag === 'input') {
    const type = ((el as HTMLInputElement).type || 'text').toLowerCase();
    return !['checkbox', 'radio', 'button', 'submit', 'reset', 'range', 'color', 'file'].includes(type);
  }
  return false;
}

function rowKeys(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[data-row-key]')].map((e) => e.dataset.rowKey!).filter(Boolean);
}

function focusRow(key: string): void {
  const el = document.querySelector<HTMLElement>(`[data-row-key="${CSS.escape(key)}"]`);
  el?.scrollIntoView({ block: 'nearest' });
}

export function moveSelection(app: AppStore, delta: number): boolean {
  const keys = rowKeys();
  if (!keys.length) return false;
  const i = app.ui.selected ? keys.indexOf(app.ui.selected) : -1;
  const next = i < 0 ? (delta > 0 ? 0 : keys.length - 1) : Math.max(0, Math.min(keys.length - 1, i + delta));
  app.select(keys[next]);
  focusRow(keys[next]);
  return true;
}

/** Handles one keydown. Returns true when the key was used. */
export function handleKey(app: AppStore, e: KeyboardEvent): boolean {
  const mod = e.ctrlKey || e.metaKey;
  if (mod && e.key.toLowerCase() === 'k') {
    app.ui.commandOpen = !app.ui.commandOpen;
    app.ui.helpOpen = false;
    return true;
  }
  if (e.key === 'Escape') {
    if (app.ui.commandOpen || app.ui.helpOpen || app.ui.confirmDelete) {
      app.ui.commandOpen = false;
      app.ui.helpOpen = false;
      app.ui.confirmDelete = null;
      return true;
    }
    if (isTypingTarget(e.target)) {
      (e.target as HTMLElement).blur();
      return true;
    }
    if (app.ui.selected) {
      app.select(null);
      return true;
    }
    return false;
  }
  if (mod || e.altKey || isTypingTarget(e.target)) return false;
  if (app.ui.commandOpen || app.ui.confirmDelete) return false;
  if (app.status !== 'ready') return false;
  const sel = app.ui.selected;
  switch (e.key) {
    case '?':
      app.ui.helpOpen = !app.ui.helpOpen;
      return true;
    case 'p':
      void app.togglePause();
      return true;
    case 'r':
      if (!sel) app.toast('Selecciona una descarga primero');
      else void app.retry(sel);
      return true;
    case 'R':
      if (!sel) app.toast('Selecciona una descarga primero');
      else void app.restart(sel);
      return true;
    case 'i':
      void app.rotate();
      return true;
    case '/': {
      const s = document.querySelector<HTMLElement>('[data-shortcut="search"]');
      if (s) s.focus();
      else app.ui.commandOpen = true;
      return true;
    }
    case 'ArrowDown':
    case 'j':
      return moveSelection(app, 1);
    case 'ArrowUp':
    case 'k':
      return moveSelection(app, -1);
    case 'ArrowRight':
    case 'ArrowLeft': {
      const r = parseKey(sel);
      if (r?.kind !== 'package') return false;
      app.toggleExpanded(r.id, e.key === 'ArrowRight');
      return true;
    }
    case 'Enter': {
      const r = parseKey(sel);
      if (r?.kind !== 'package' || (e.target as HTMLElement)?.closest?.('button, a')) return false;
      app.toggleExpanded(r.id);
      return true;
    }
    case 'Delete':
      if (!sel) return false;
      app.askDelete(sel);
      return true;
  }
  return false;
}

export function installShortcuts(app: AppStore): () => void {
  const on = (e: KeyboardEvent) => {
    if (handleKey(app, e)) e.preventDefault();
  };
  window.addEventListener('keydown', on);
  return () => window.removeEventListener('keydown', on);
}
