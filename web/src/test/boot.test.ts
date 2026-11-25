// The no-flash boot script runs before Svelte: test it with a fake DOM.
import { describe, expect, it } from 'vitest';
import { BOOT_JS, STORAGE_KEY } from '../theme/boot';
import { bootTable, THEMES } from '../theme';

function run(stored: string | null | Error, prefersLight = false) {
  const attrs: Record<string, string> = {};
  const props: Record<string, string> = {};
  const style = { setProperty: (k: string, v: string) => (props[k] = v), backgroundColor: '', color: '', colorScheme: '' };
  const document = { documentElement: { setAttribute: (k: string, v: string) => (attrs[k] = v), style } };
  const localStorage = {
    getItem: (k: string) => {
      if (stored instanceof Error) throw stored;
      return k === STORAGE_KEY ? stored : null;
    },
  };
  const window = { matchMedia: (q: string) => ({ matches: prefersLight && q.includes('light') }) };
  new Function('document', 'localStorage', 'window', `(${BOOT_JS})(${JSON.stringify(bootTable())});`)(document, localStorage, window);
  return { attrs, props, style };
}

describe('boot script', () => {
  it('defaults to grafito + sistema (dark system) with the default accent', () => {
    const r = run(null);
    expect(r.attrs).toEqual({ 'data-estilo': 'grafito', 'data-tema': 'sistema', 'data-esquema': 'oscuro', 'data-acento': '#6ea8ff' });
    expect(r.style.backgroundColor).toBe(THEMES.grafito.oscuro.bg);
    expect(r.style.colorScheme).toBe('dark');
    expect(r.props['--accent']).toBe('#6ea8ff');
  });
  it('follows prefers-color-scheme when tema is sistema', () => {
    expect(run(JSON.stringify({ estilo: 'enfoque', tema: 'sistema' }), true).attrs['data-esquema']).toBe('claro');
  });
  it('falls back to the default accent when the stored one fails in this scheme', () => {
    const r = run(JSON.stringify({ estilo: 'monocromo', tema: 'claro', acento: '#c6ff3d' }));
    expect(r.attrs['data-acento']).toBe(THEMES.monocromo.claro.accent);
    const ok = run(JSON.stringify({ estilo: 'monocromo', tema: 'oscuro', acento: '#c6ff3d' }));
    expect(ok.attrs['data-acento']).toBe('#c6ff3d');
    expect(ok.props['--on-accent']).toBe('#000000');
  });
  it('survives corrupt or blocked storage and unknown styles', () => {
    expect(run('{nope').attrs['data-estilo']).toBe('grafito');
    expect(run(new Error('SecurityError')).attrs['data-estilo']).toBe('grafito');
    expect(run(JSON.stringify({ estilo: 'otro' })).attrs['data-estilo']).toBe('grafito');
  });
});
