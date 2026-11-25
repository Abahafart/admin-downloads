// The 5 styles (PLAN.md 7.1): every registered style is complete (own Shell
// and DownloadsView), follows the shell/keyboard contract,
// and the pure view helpers shared by Terminal/Medianoche/Monocromo/Enfoque
// format like the designs.
import { describe, expect, it } from 'vitest';
import { STYLES, STYLE_LIST, getStyle } from '../styles/registry';
import { ESTILOS } from '../theme/types';
import { SHARED_SCREENS, screenFor } from '../screens';
import { seed } from '../lib/mock/data';
import { buildItems } from '../lib/store/rows';
import {
  charBar, compactBytes, compactSpeed, doneOf, egressPhrase, failSentence, focusItem, glyph, humanDuration, itemVM, ongoingSummary,
  padPct, shortEgress, splitSpeed, termState, waitSentence,
} from '../styles/common/model';

const SOURCES = import.meta.glob('../styles/*/*.svelte', { query: '?raw', import: 'default', eager: true }) as Record<string, string>;
const src = (id: string, file: string) => SOURCES[`../styles/${id}/${file}.svelte`] ?? '';

describe('style registry', () => {
  it('has exactly the 5 styles, in order, with letters A-E', () => {
    expect(Object.keys(STYLES).sort()).toEqual([...ESTILOS].sort());
    expect(STYLE_LIST.map((s) => s.id)).toEqual([...ESTILOS]);
    expect(STYLE_LIST.map((s) => s.letra)).toEqual(['A', 'B', 'C', 'D', 'E']);
  });

  it.each(ESTILOS)('%s is complete', (id) => {
    const s = STYLES[id];
    expect(s.id).toBe(id);
    expect('provisional' in s).toBe(false); // the placeholder flag is gone
    expect(typeof s.Shell).toBe('function');
    expect(typeof s.DownloadsView).toBe('function');
    expect(s.nombre).toBeTruthy();
    expect(s.descripcion).toBeTruthy();
    expect(['sidebar', 'toplinks-bar', 'tabs-cards', 'column-list', 'toplinks-focus']).toContain(s.thumb);
  });

  it('no style borrows another style structure', () => {
    const shells = new Set(STYLE_LIST.map((s) => s.Shell));
    const views = new Set(STYLE_LIST.map((s) => s.DownloadsView));
    expect(shells.size).toBe(5);
    expect(views.size).toBe(5);
  });

  it('only Terminal shows the shortcuts always; the 4 new styles have their own thumbnail', () => {
    expect(STYLE_LIST.filter((s) => s.shortcutsVisible).map((s) => s.id)).toEqual(['terminal']);
    for (const id of ['terminal', 'medianoche', 'monocromo', 'enfoque'] as const) expect(typeof STYLES[id].Thumbnail).toBe('function');
    expect(typeof STYLES.terminal.CommandBar).toBe('function');
  });

  it('screens resolve to the style view or the shared screens', () => {
    for (const s of STYLE_LIST) {
      expect(screenFor(s, 'descargas')).toBe(s.DownloadsView);
      for (const k of Object.keys(SHARED_SCREENS) as (keyof typeof SHARED_SCREENS)[]) {
        expect(screenFor(s, k)).toBe(s.screens?.[k] ?? SHARED_SCREENS[k]);
      }
    }
    expect(getStyle('nope').id).toBe('grafito');
  });
});

describe('shell and keyboard contract (source checks)', () => {
  it.each(ESTILOS)('%s: Shell renders children inside <main id="contenido">', (id) => {
    const shell = src(id, 'Shell');
    expect(shell).toMatch(/<main[^>]*id="contenido"[\s\S]*\{@render children\(\)\}[\s\S]*<\/main>/);
    expect(shell).toContain('aria-label="Principal"');
    // Fixed status: total speed, IP and clipboard (PLAN.md 7.1).
    expect(shell + src(id, 'ClipToggle')).toMatch(/app\.totalSpeed|LiveStatus/);
    expect(shell + src(id, 'ClipToggle')).toMatch(/app\.publicIP|LiveStatus/);
    expect(shell + src(id, 'ClipToggle')).toMatch(/app\.clipboard|LiveStatus/);
  });

  it.each(ESTILOS)('%s: Descargas marks selectable rows and has a search field', (id) => {
    const view = src(id, 'DownloadsView');
    expect(view).toContain('data-row-key=');
    expect(view + src(id, 'Prompt')).toContain('data-shortcut="search"');
  });

  it('Terminal shows p, r, R, i and / in its bottom bar and the clipboard switch', () => {
    const shell = src('terminal', 'Shell');
    for (const k of ['p', 'r', 'R', 'i', '/']) expect(shell).toContain(`<span class="k">${k}</span>`);
    expect(shell).toContain('portapapeles:');
  });
});

describe('view helpers', () => {
  const now = Date.now();
  const db = seed(now);
  const items = buildItems(Object.fromEntries(db.downloads), Object.fromEntries(db.packages));
  const vms = items.map((it) => itemVM(it, now));
  const byName = (n: string) => vms.find((v) => v.name === n)!;

  it('character bars match the Terminal design (19 cells)', () => {
    expect(charBar(62)).toEqual({ fill: '█'.repeat(12), empty: '░'.repeat(7) });
    expect(charBar(34).fill).toHaveLength(6);
    expect(charBar(47).fill).toHaveLength(9);
    expect(charBar(18).fill).toHaveLength(3);
    expect(charBar(0)).toEqual({ fill: '', empty: '░'.repeat(19) });
    expect(charBar(100).empty).toBe('');
    expect(charBar(250).fill).toHaveLength(19);
    expect(padPct(0)).toBe('  0%');
    expect(padPct(62)).toBe(' 62%');
    expect(padPct(100)).toBe('100%');
  });

  it('CLI sizes and speeds', () => {
    expect(compactBytes(4.2e9)).toBe('4.2G');
    expect(compactBytes(380e6)).toBe('380M');
    expect(compactBytes(14e6)).toBe('14M');
    expect(compactBytes(-1)).toBe('—');
    expect(compactSpeed(18.4e6)).toBe('18.4M/s');
    expect(compactSpeed(0)).toBe('—');
  });

  it('glyphs and terminal states', () => {
    expect(['downloading', 'waiting', 'paused', 'failed', 'queued', 'completed'].map((s) => glyph(s as never)).join('')).toBe('▸◷‖✕·✓');
    expect(termState(byName('Album_2019.rar').st)).toMatch(/^cuota ip \d\d:\d\d$/);
    expect(termState(byName('backup_fotos.7z').st)).toBe('pausada');
    expect(termState(byName('instalador.iso').st)).toBe('en cola');
    expect(shortEgress('wg:mullvad-se', Object.fromEntries(db.egress))).toBe('wg');
    expect(shortEgress('tor', {}, { tor: true })).toBe('tor→rotando');
    expect(egressPhrase('direct', {})).toBe('directa');
    expect(egressPhrase('socks5:proxy-1', {})).toBe('proxy SOCKS5');
  });

  it('sentences: Drive file quota explains that rotating does not help', () => {
    const v = byName('video_presentacion.mp4');
    expect(v.st.fileQuota).toBe(true);
    expect(failSentence(v, false)).toBe('Cuota del archivo excedida. Cambiar de IP no ayuda: conecta una cuenta de Drive para copiarlo.');
    expect(failSentence(v, true)).toMatch(/Cambiar de IP no ayuda/);
    expect(waitSentence(byName('Album_2019.rar'), {}, { tor: true })).toBe('Cuota por IP agotada · rotando circuito');
  });

  it('numbers in Spanish', () => {
    expect(doneOf(2.6e9, 4.2e9)).toEqual({ done: '2,6', total: '4,2 GB' });
    expect(doneOf(0.414e9, 2.3e9)).toEqual({ done: '0,4', total: '2,3 GB' });
    expect(humanDuration(84)).toBe('1 min 24 s');
    expect(humanDuration(45)).toBe('45 s');
    expect(humanDuration(3720)).toBe('1 h 2 min');
    expect(humanDuration(-1)).toBe('—');
    expect(splitSpeed(24.5e6)).toEqual({ num: '24,5', unit: 'MB/s' });
    expect(splitSpeed(0)).toEqual({ num: '0', unit: 'MB/s' });
    expect(ongoingSummary({ todas: 7, activas: 2, espera: 1, pausadas: 1, fallidas: 1, completadas: 1, cola: 1 })).toBe('2 activas · 1 en espera · 3 detenidas');
  });

  it('folder packages aggregate their files and the focus is the active download', () => {
    const curso = byName('Curso_Fotografia');
    expect(curso.kind).toBe('package');
    expect(curso.filesTotal).toBe(12);
    expect(curso.children).toHaveLength(12);
    expect(curso.key).toBe('p:1');
    expect(focusItem(vms)?.st.state).toBe('downloading');
    expect(focusItem(vms.filter((v) => v.st.state !== 'downloading'))?.name).toBe('Album_2019.rar');
  });
});
