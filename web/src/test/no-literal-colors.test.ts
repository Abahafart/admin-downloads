// Fails if any component, screen or style file uses a literal color instead
// of a token (PLAN.md 7.1 "Ningún componente usa un color literal"). Only
// the token files src/theme/<estilo>.ts may contain color values.
import { describe, expect, it } from 'vitest';

// Every source file as text (Vite glob import), keyed like "../styles/x.svelte".
const SOURCES = import.meta.glob(['../**/*.svelte', '../**/*.css', '../**/*.ts', '!../**/*.test.ts'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;
const ALLOWED = new Set(['theme/grafito.ts', 'theme/terminal.ts', 'theme/medianoche.ts', 'theme/monocromo.ts', 'theme/enfoque.ts']);

const HEX = /(?<![\w&/-])#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})(?![\w-])/g;
const FUNCS = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/g;
const NAMED = /(?<![\w-])(?:white|black|red|green|blue|yellow|orange|purple|pink|gray|grey|silver|maroon|navy|teal|olive|lime|aqua|fuchsia|cyan|magenta|gold|beige|brown|crimson|indigo|violet|tomato|coral|salmon|khaki|ivory|azure|lavender|turquoise|tan|plum|orchid)(?![\w-])/g;

/** CSS that a file contains: .css files, <style> blocks and style attributes/directives. */
function cssParts(file: string, text: string): string[] {
  if (file.endsWith('.css')) return [text];
  if (!file.endsWith('.svelte')) return [];
  const parts: string[] = [];
  for (const m of text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) parts.push(m[1]);
  for (const m of text.matchAll(/\sstyle="([^"]*)"/g)) parts.push(m[1]);
  for (const m of text.matchAll(/\sstyle:[\w-]+="([^"]*)"/g)) parts.push(m[1]);
  return parts;
}

function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
}

const files = Object.keys(SOURCES)
  .map((k) => k.replace(/^\.\.\//, ''))
  .filter((f) => !ALLOWED.has(f))
  .sort();

describe('no literal colors outside src/theme/<estilo>.ts', () => {
  it('scans a meaningful set of files', () => {
    expect(files.length).toBeGreaterThan(20);
    expect(files).toContain('styles/grafito/DownloadsView.svelte');
  });

  it.each(files)('%s', (f) => {
    const text = stripComments(SOURCES['../' + f]);
    const problems: string[] = [];
    for (const m of text.matchAll(HEX)) problems.push(`hex ${m[0]}`);
    for (const css of cssParts(f, text)) {
      for (const m of css.matchAll(FUNCS)) problems.push(`color function ${m[0]}`);
      for (const m of css.matchAll(NAMED)) problems.push(`named color ${m[0]}`);
    }
    expect(problems).toEqual([]);
  });
});

describe('the literal-color detector itself', () => {
  it('catches hex, functions and named colors in CSS', () => {
    const sample = '<div style="color: #fff"></div><style>.a{background:rgb(0,0,0);border-color:white}</style>';
    expect([...sample.matchAll(HEX)].length).toBe(1);
    const css = cssParts('x.svelte', sample).join('\n');
    expect([...css.matchAll(FUNCS)].length).toBe(1);
    expect([...css.matchAll(NAMED)].length).toBe(1);
  });
  it('ignores tokens, routes and ids', () => {
    const sample = '<a href="#/red">x</a><style>.a{color:var(--text);background:transparent} #contenido{}</style>';
    expect([...sample.matchAll(HEX)].length).toBe(0);
    expect([...cssParts('x.svelte', sample).join('').matchAll(NAMED)].length).toBe(0);
  });
});
