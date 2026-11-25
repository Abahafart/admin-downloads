// E · Enfoque: values from docs/design/Enfoque.dc.html renderVals().t.
// raised, text2 and done are not in the design and are derived.
import type { StyleTheme } from './types';

const enfoque: StyleTheme = {
  id: 'enfoque',
  font: "'Manrope', system-ui, sans-serif",
  fontMono: "'JetBrains Mono', ui-monospace, Menlo, monospace",
  fontSize: 14,
  radius: { sm: 6, md: 8, lg: 12, pill: 999 },
  accents: ['#62cdb4', '#0d6e5e', '#f0a35e', '#a4532a'],
  accentAsText: true,
  inks: ['#131211', '#ffffff'],
  oscuro: {
    bg: '#131211', surface: '#1d1b19', raised: '#262421',
    border: '#262421', borderStrong: '#3a3733',
    text: '#efebe5', text2: '#cfc9c0', muted: '#a09a91',
    track: '#2a2825', accent: '#62cdb4',
    warn: '#eaa860', err: '#f2937f', paused: '#6c675f', done: '#3a3733',
  },
  claro: {
    bg: '#faf8f5', surface: '#efebe5', raised: '#e6e1d9',
    border: '#e6e1d9', borderStrong: '#cfc8bd',
    text: '#1c1a17', text2: '#3a362f', muted: '#625c53',
    track: '#e6e1d9', accent: '#0d6e5e',
    warn: '#8c4f00', err: '#ae2d22', paused: '#a39c90', done: '#cfc8bd',
  },
};

export default enfoque;
