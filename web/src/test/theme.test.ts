// Appearance tests (PLAN.md 14 "Apariencia"): every text token against its
// backgrounds >= 4.5:1 in the 10 style x theme pairs, plus the accent rules.
import { describe, expect, it } from 'vitest';
import { THEMES, accentStatus, bootTable, contrast, cssVar, effectiveAccent, onAccent, schemeVars, themeCSS, MIN_TEXT, MIN_UI } from '../theme';
import { ESTILOS, type Esquema } from '../theme/types';

const ESQUEMAS: Esquema[] = ['oscuro', 'claro'];
const TEXT_TOKENS = ['text', 'text2', 'muted', 'warn', 'err'] as const;
const PAIRS = ESTILOS.flatMap((e) => ESQUEMAS.map((s) => [e, s] as const));

describe('tokens: 10 style x theme pairs', () => {
  it('there are exactly 5 styles x 2 schemes', () => {
    expect(Object.keys(THEMES).sort()).toEqual([...ESTILOS].sort());
    expect(PAIRS).toHaveLength(10);
  });

  it.each(PAIRS)('%s · %s: every text token >= 4.5:1 on bg/surface/raised and extra backgrounds', (estilo, esquema) => {
    const style = THEMES[estilo];
    const t = style[esquema];
    const backgrounds: Record<string, string> = { bg: t.bg, surface: t.surface, raised: t.raised };
    for (const name of style.textOn ?? []) {
      const v = t.extra?.[name];
      expect(v, `${estilo}/${esquema}: textOn ${name} must be defined in extra`).toBeDefined();
      backgrounds[name] = v!;
    }
    const failures: string[] = [];
    for (const tok of TEXT_TOKENS) {
      for (const [bn, bv] of Object.entries(backgrounds)) {
        const c = contrast(t[tok], bv);
        if (c < MIN_TEXT) failures.push(`${tok} on ${bn}: ${c.toFixed(2)}`);
      }
    }
    expect(failures).toEqual([]);
  });

  it.each(PAIRS)('%s · %s: default accent is one of the 4 and passes its rule', (estilo, esquema) => {
    const style = THEMES[estilo];
    expect(style.accents).toHaveLength(4);
    expect(style.accents).toContain(style[esquema].accent);
    const st = accentStatus(style, esquema, style[esquema].accent);
    expect(st.ok, st.reason).toBe(true);
    expect(contrast(st.onAccent, st.accent)).toBeGreaterThanOrEqual(MIN_TEXT);
  });

  it.each(PAIRS)('%s · %s: every accent follows the 3:1 / 4.5:1 rule', (estilo, esquema) => {
    const style = THEMES[estilo];
    const t = style[esquema];
    const min = style.accentAsText ? MIN_TEXT : MIN_UI;
    for (const a of style.accents) {
      const st = accentStatus(style, esquema, a);
      const ink = onAccent(a, style.inks);
      const expected = contrast(a, t.bg) >= min && contrast(a, t.surface) >= min && contrast(a, ink) >= MIN_TEXT;
      expect(st.ok, `${a}: ${st.reason}`).toBe(expected);
      expect(st.onAccent).toBe(ink);
      if (!st.ok) expect(st.reason).toMatch(/Contraste .*:1 con .* \(mínimo/);
      // --on-accent is black or white by contrast: the chosen ink is never worse.
      expect(contrast(a, st.onAccent)).toBeGreaterThanOrEqual(Math.min(contrast(a, style.inks[0]), contrast(a, style.inks[1])));
    }
  });

  it('known disabled accents (PLAN.md example: #c6ff3d in Monocromo claro)', () => {
    expect(accentStatus(THEMES.monocromo, 'claro', '#c6ff3d').ok).toBe(false);
    expect(accentStatus(THEMES.monocromo, 'oscuro', '#0a0a0a').ok).toBe(false);
    expect(accentStatus(THEMES.grafito, 'claro', '#6ea8ff').ok).toBe(false);
    expect(accentStatus(THEMES.grafito, 'oscuro', '#6ea8ff').ok).toBe(true);
  });

  it('a chosen accent that fails falls back to the scheme default', () => {
    expect(effectiveAccent(THEMES.monocromo, 'claro', '#c6ff3d').accent).toBe(THEMES.monocromo.claro.accent);
    expect(effectiveAccent(THEMES.monocromo, 'oscuro', '#c6ff3d').accent).toBe('#c6ff3d');
    expect(effectiveAccent(THEMES.grafito, 'oscuro', '#123456').accent).toBe('#6ea8ff');
    expect(effectiveAccent(THEMES.grafito, 'claro', '').accent).toBe('#1f5fd1');
  });

  it('generated CSS has every semantic token for each pair', () => {
    const css = themeCSS();
    const required = ['--bg', '--surface', '--raised', '--border', '--border-strong', '--text', '--text-2', '--muted', '--track', '--accent', '--on-accent', '--warn', '--err', '--paused', '--done', '--font', '--font-mono'];
    for (const [e, s] of PAIRS) {
      const rule = css.split('\n').find((l) => l.startsWith(`[data-estilo="${e}"][data-esquema="${s}"]{`));
      expect(rule, `${e}/${s}`).toBeDefined();
      for (const v of required) expect(rule).toContain(`${v}:`);
      expect(rule).toContain(`color-scheme:${s === 'oscuro' ? 'dark' : 'light'}`);
    }
    expect(cssVar('borderStrong')).toBe('--border-strong');
    expect(cssVar('text2')).toBe('--text-2');
    expect(schemeVars(THEMES.grafito, 'oscuro')['--side']).toBe('#0b0c0e');
  });

  it('boot table only lists accents that pass, with their ink', () => {
    const tbl = bootTable();
    for (const [e, s] of PAIRS) {
      const entry = tbl.styles[e][s];
      for (const a of THEMES[e].accents) {
        const st = accentStatus(THEMES[e], s, a);
        if (st.ok) expect(entry.acc[a]).toBe(st.onAccent);
        else expect(entry.acc[a]).toBeUndefined();
      }
      expect(entry.bg).toBe(THEMES[e][s].bg);
    }
  });
});
