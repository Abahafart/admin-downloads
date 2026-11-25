// Live appearance state: applies data-estilo / data-tema / data-esquema /
// data-acento and the accent variables on <html>, follows the system scheme
// while tema = "sistema", and mirrors the preference to localStorage for the
// boot script. The source of truth is settings.appearance on the server;
// the store calls setAppearance() when settings load or change.
import { THEMES, DEFAULT_ESTILO, themeCSS, effectiveAccent, type AccentStatus, type Esquema, type EstiloId, type Tema, accentStatus } from './index';
import { STORAGE_KEY } from './boot';

export interface Appearance {
  estilo: EstiloId;
  tema: Tema;
  /** Chosen accent hex ("" = style default for the active scheme). */
  acento: string;
}

function systemScheme(): Esquema {
  try {
    return typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: light)').matches ? 'claro' : 'oscuro';
  } catch {
    return 'oscuro';
  }
}

export function normalizeAppearance(a: Partial<Appearance> | null | undefined): Appearance {
  const estilo = a?.estilo && a.estilo in THEMES ? a.estilo : DEFAULT_ESTILO;
  const tema: Tema = a?.tema === 'oscuro' || a?.tema === 'claro' ? a.tema : 'sistema';
  const acento = typeof a?.acento === 'string' ? a.acento.toLowerCase() : '';
  return { estilo, tema, acento };
}

function readStored(): Appearance {
  try {
    return normalizeAppearance(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? 'null'));
  } catch {
    return normalizeAppearance(null);
  }
}

class AppearanceState {
  pref = $state<Appearance>(readStored());
  system = $state<Esquema>(systemScheme());

  get esquema(): Esquema {
    return this.pref.tema === 'sistema' ? this.system : this.pref.tema;
  }
  get theme() {
    return THEMES[this.pref.estilo];
  }
  /** Effective accent (falls back to the default if the chosen one fails). */
  get accent(): AccentStatus {
    return effectiveAccent(this.theme, this.esquema, this.pref.acento);
  }
  /** The 4 accents of the active style with their status for this scheme. */
  get accents(): AccentStatus[] {
    return this.theme.accents.map((a) => accentStatus(this.theme, this.esquema, a));
  }
}

export const appearance = new AppearanceState();

let installed = false;

/** Injects the token stylesheet and starts following the system scheme. */
export function installTheme(): void {
  if (installed || typeof document === 'undefined') return;
  installed = true;
  const style = document.createElement('style');
  style.id = 'adesc-theme';
  style.textContent = themeCSS();
  document.head.appendChild(style);
  try {
    const mq = matchMedia('(prefers-color-scheme: light)');
    const on = () => {
      appearance.system = mq.matches ? 'claro' : 'oscuro';
      apply();
    };
    if (mq.addEventListener) mq.addEventListener('change', on);
    else mq.addListener(on);
  } catch {
    /* no matchMedia: stay dark */
  }
  apply();
  // The boot script painted html with an inline background; base.css takes
  // over now that the tokens exist.
  document.documentElement.style.removeProperty('background-color');
  document.documentElement.style.removeProperty('color');
}

function apply(): void {
  if (typeof document === 'undefined') return;
  const d = document.documentElement;
  const acc = appearance.accent;
  d.dataset.estilo = appearance.pref.estilo;
  d.dataset.tema = appearance.pref.tema;
  d.dataset.esquema = appearance.esquema;
  d.dataset.acento = acc.accent;
  d.style.setProperty('--accent', acc.accent);
  d.style.setProperty('--on-accent', acc.onAccent);
  d.style.colorScheme = appearance.esquema === 'oscuro' ? 'dark' : 'light';
  // Browser chrome / Wails window background follow --bg.
  const bg = appearance.theme[appearance.esquema].bg;
  let meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (!meta) {
    meta = document.createElement('meta');
    meta.name = 'theme-color';
    document.head.appendChild(meta);
  }
  meta.content = bg;
  d.dispatchEvent(new CustomEvent('adesc-appearance', { detail: { ...appearance.pref, esquema: appearance.esquema, bg } }));
}

/** Applies and mirrors a new preference (does not save it on the server). */
export function setAppearance(a: Partial<Appearance>): Appearance {
  const next = normalizeAppearance({ ...appearance.pref, ...a });
  appearance.pref = next;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    /* private mode / storage disabled */
  }
  apply();
  return next;
}
