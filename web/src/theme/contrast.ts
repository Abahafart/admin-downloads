// WCAG 2.x relative luminance and contrast ratio, plus the accent rules of
// PLAN.md section 7.1. Pure functions shared by the runtime, the boot script
// table and the tests.
import type { Esquema, SchemeTheme, StyleTheme } from './types';

export function parseHex(hex: string): [number, number, number] {
  let h = hex.trim().replace(/^#/, '');
  if (h.length === 3) h = h.split('').map((c) => c + c).join('');
  if (!/^[0-9a-fA-F]{6}$/.test(h)) throw new Error(`color inválido: ${hex}`);
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
}

export function luminance(hex: string): number {
  const [r, g, b] = parseHex(hex).map((v) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** Picks the ink (dark or light) with more contrast against the accent. */
export function onAccent(accent: string, inks: readonly [string, string]): string {
  return contrast(accent, inks[0]) >= contrast(accent, inks[1]) ? inks[0] : inks[1];
}

export const MIN_TEXT = 4.5;
export const MIN_UI = 3;

export interface AccentStatus {
  accent: string;
  ok: boolean;
  /** Spanish, user-facing; empty when ok. */
  reason: string;
  onAccent: string;
}

const fmt = (n: number) => n.toFixed(1).replace('.', ',');

/** Applies the accent rules for one style and scheme. */
export function accentStatus(style: StyleTheme, esquema: Esquema, accent: string): AccentStatus {
  const t: SchemeTheme = style[esquema];
  const min = style.accentAsText ? MIN_TEXT : MIN_UI;
  const ink = onAccent(accent, style.inks);
  const checks: [string, number, number][] = [
    ['el fondo', contrast(accent, t.bg), min],
    ['la superficie', contrast(accent, t.surface), min],
    ['su texto', contrast(accent, ink), MIN_TEXT],
  ];
  for (const [what, ratio, need] of checks) {
    if (ratio < need) {
      return {
        accent,
        ok: false,
        onAccent: ink,
        reason: `Contraste ${fmt(ratio)}:1 con ${what} en el tema ${esquema} (mínimo ${fmt(need)}:1)`,
      };
    }
  }
  return { accent, ok: true, reason: '', onAccent: ink };
}

/**
 * Returns the accent to use: the chosen one if it passes, else the scheme's
 * default (PLAN.md: "si estaba elegido, se usa el acento por defecto").
 */
export function effectiveAccent(style: StyleTheme, esquema: Esquema, chosen: string | undefined | null): AccentStatus {
  const want = (chosen ?? '').toLowerCase();
  if (want && style.accents.includes(want)) {
    const st = accentStatus(style, esquema, want);
    if (st.ok) return st;
  }
  return accentStatus(style, esquema, style[esquema].accent);
}
