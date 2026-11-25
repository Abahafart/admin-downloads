// Command bar (ctrl+k) and keyboard shortcuts, shared by every style.
// Terminal shows SHORTCUTS in its bottom bar; the other styles in the "?"
// help overlay. Commands also accept pasted text with links.
import type { AppStore } from './store/app.svelte';
import type { ScreenId } from './router.svelte';
import { THEMES, type EstiloId } from '../theme';

export interface Command {
  /** What the user types, with the slash: "/pausa". */
  name: string;
  aliases?: string[];
  label: string;
  /** Optional argument hint shown in the list ("claro|oscuro|sistema"). */
  arg?: string;
  run(app: AppStore, arg: string): Promise<unknown> | unknown;
}

const nav = (screen: ScreenId, name: string, label: string): Command => ({ name, label, run: (app) => app.go(screen) });

export const COMMANDS: Command[] = [
  { name: '/pausa', aliases: ['/pausar', '/pausa todo'], label: 'Pausar o reanudar todas las descargas', run: (app) => app.togglePauseAll() },
  { name: '/reanudar', label: 'Reanudar todas las descargas', run: (app) => app.resumeAll() },
  { name: '/rotar', aliases: ['/rotar ip', '/ip'], label: 'Rotar la IP ahora', run: (app) => app.rotate('') },
  { name: '/reintentar fallidas', aliases: ['/reintentar'], label: 'Reintentar todas las descargas fallidas', run: (app) => app.retryFailed() },
  nav('descargas', '/descargas', 'Ir a Descargas'),
  nav('linkgrabber', '/linkgrabber', 'Ir al LinkGrabber'),
  nav('cuentas', '/cuentas', 'Ir a Cuentas'),
  nav('red', '/red', 'Ir a Red y rotación'),
  nav('ajustes', '/ajustes', 'Ir a Ajustes'),
  {
    name: '/tema',
    arg: 'sistema | oscuro | claro',
    label: 'Cambiar el tema',
    run: (app, arg) => {
      const t = arg.trim().toLowerCase();
      if (t === 'sistema' || t === 'oscuro' || t === 'claro') return app.setAppearance({ tema: t });
      app.toast('Usa /tema sistema, /tema oscuro o /tema claro', 'warning');
    },
  },
  {
    name: '/estilo',
    arg: Object.keys(THEMES).join(' | '),
    label: 'Cambiar el estilo',
    run: (app, arg) => {
      const e = arg.trim().toLowerCase();
      if (e in THEMES) return app.setAppearance({ estilo: e as EstiloId });
      app.toast(`Estilos: ${Object.keys(THEMES).join(', ')}`, 'warning');
    },
  },
];

const URL_RE = /https?:\/\/\S+/i;

export function hasLinks(text: string): boolean {
  return URL_RE.test(text);
}

function norm(s: string): string {
  return s.trim().toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, '');
}

/** Commands matching what has been typed so far (for the suggestion list). */
export function suggest(input: string): Command[] {
  const q = norm(input);
  if (!q || hasLinks(input)) return q ? [] : COMMANDS;
  const withSlash = q.startsWith('/') ? q : '/' + q;
  return COMMANDS.filter((c) => [c.name, ...(c.aliases ?? [])].some((n) => norm(n).startsWith(withSlash) || withSlash.startsWith(norm(n) + ' ')) || norm(c.label).includes(q));
}

/** Finds the command for a full line, longest name first ("/reintentar fallidas" before "/reintentar"). */
export function parseCommand(input: string): { cmd: Command; arg: string } | null {
  const q = norm(input);
  const withSlash = q.startsWith('/') ? q : '/' + q;
  let best: { cmd: Command; arg: string; len: number } | null = null;
  for (const c of COMMANDS) {
    for (const n of [c.name, ...(c.aliases ?? [])]) {
      const nn = norm(n);
      if (withSlash === nn || withSlash.startsWith(nn + ' ')) {
        if (!best || nn.length > best.len) best = { cmd: c, arg: input.trim().slice(nn.length).trim(), len: nn.length };
      }
    }
  }
  return best ? { cmd: best.cmd, arg: best.arg } : null;
}

/** Runs a command line or adds the pasted links. Returns false if not understood. */
export async function runLine(app: AppStore, input: string): Promise<boolean> {
  if (hasLinks(input)) {
    await app.addLinks(input);
    return true;
  }
  const p = parseCommand(input);
  if (!p) {
    app.toast(`No conozco «${input.trim()}». Prueba /pausa, /rotar o /reintentar fallidas`, 'warning');
    return false;
  }
  await p.cmd.run(app, p.arg);
  return true;
}
