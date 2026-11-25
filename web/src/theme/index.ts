// Theme registry (pure data + CSS generation). One token file per style;
// adding a style's colors = adding <estilo>.ts and listing it here.
import type { ColorTokens, Esquema, EstiloId, SchemeTheme, StyleTheme } from './types';
import { accentStatus, onAccent } from './contrast';
import grafito from './grafito';
import terminal from './terminal';
import medianoche from './medianoche';
import monocromo from './monocromo';
import enfoque from './enfoque';

export * from './types';
export * from './contrast';

export const THEMES: Record<EstiloId, StyleTheme> = { grafito, terminal, medianoche, monocromo, enfoque };
export const DEFAULT_ESTILO: EstiloId = 'grafito';

export const COLOR_KEYS: readonly (keyof ColorTokens)[] = [
  'bg', 'surface', 'raised', 'border', 'borderStrong', 'text', 'text2', 'muted',
  'track', 'accent', 'warn', 'err', 'paused', 'done',
];

/** camelCase token key -> CSS custom property name. */
export function cssVar(key: string): string {
  return '--' + key.replace(/([a-z])([A-Z0-9])/g, '$1-$2').toLowerCase();
}

/** All CSS custom properties of one style+scheme, --on-accent included. */
export function schemeVars(style: StyleTheme, esquema: Esquema): Record<string, string> {
  const t: SchemeTheme = style[esquema];
  const out: Record<string, string> = {};
  for (const k of COLOR_KEYS) out[cssVar(k)] = t[k];
  out['--on-accent'] = onAccent(t.accent, style.inks);
  for (const [k, v] of Object.entries(t.extra ?? {})) out[k] = v;
  out['--font'] = style.font;
  out['--font-mono'] = style.fontMono;
  out['--font-size'] = `${style.fontSize}px`;
  out['--radius-sm'] = `${style.radius.sm}px`;
  out['--radius-md'] = `${style.radius.md}px`;
  out['--radius-lg'] = `${style.radius.lg}px`;
  out['--radius-pill'] = `${style.radius.pill}px`;
  return out;
}

/**
 * The stylesheet with every style x scheme. Selectors are not tied to :root,
 * so any element with data-estilo + data-esquema (style thumbnails) gets that
 * theme's tokens.
 */
export function themeCSS(): string {
  const rules: string[] = [];
  for (const style of Object.values(THEMES)) {
    for (const esquema of ['oscuro', 'claro'] as const) {
      const vars = Object.entries(schemeVars(style, esquema)).map(([k, v]) => `${k}:${v}`).join(';');
      const scheme = esquema === 'oscuro' ? 'dark' : 'light';
      rules.push(`[data-estilo="${style.id}"][data-esquema="${esquema}"]{${vars};color-scheme:${scheme}}`);
    }
  }
  return rules.join('\n');
}

/** Compact table embedded in index.html for the no-flash boot script. */
export interface BootTable {
  def: EstiloId;
  styles: Record<string, { [E in Esquema]: { bg: string; text: string; acc: Record<string, string>; defAcc: string; defInk: string } }>;
}

export function bootTable(): BootTable {
  const styles: BootTable['styles'] = {};
  for (const style of Object.values(THEMES)) {
    const entry = {} as BootTable['styles'][string];
    for (const esquema of ['oscuro', 'claro'] as const) {
      const acc: Record<string, string> = {};
      for (const a of style.accents) {
        const st = accentStatus(style, esquema, a);
        if (st.ok) acc[a] = st.onAccent; // only valid accents, with their ink
      }
      entry[esquema] = {
        bg: style[esquema].bg,
        text: style[esquema].text,
        acc,
        defAcc: style[esquema].accent,
        defInk: onAccent(style[esquema].accent, style.inks),
      };
    }
    styles[style.id] = entry;
  }
  return { def: DEFAULT_ESTILO, styles };
}
