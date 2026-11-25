// A · Grafito: values from docs/design/Grafito.dc.html renderVals().t
// (border2 -> borderStrong, text2 -> text2, side/rowHi -> extra tokens).
import type { StyleTheme } from './types';

const grafito: StyleTheme = {
  id: 'grafito',
  font: "'Inter', system-ui, sans-serif",
  fontMono: "'JetBrains Mono', ui-monospace, Menlo, monospace",
  fontSize: 13,
  radius: { sm: 4, md: 6, lg: 8, pill: 15 },
  accents: ['#6ea8ff', '#1f5fd1', '#7cc4b4', '#0f7a68'],
  accentAsText: false,
  inks: ['#0b0c0e', '#ffffff'],
  textOn: ['--side', '--row-hi'],
  oscuro: {
    bg: '#0e0f11', surface: '#111316', raised: '#1a1d21',
    border: '#202327', borderStrong: '#2c3035',
    text: '#e8eaed', text2: '#c4c8cd', muted: '#9aa0a6',
    track: '#24272b', accent: '#6ea8ff',
    warn: '#e0a64a', err: '#f08a7e', paused: '#6b7178', done: '#3a3f45',
    extra: { '--side': '#0b0c0e', '--row-hi': '#15181c' },
  },
  claro: {
    bg: '#f6f7f8', surface: '#ffffff', raised: '#e3e6ea',
    border: '#e1e4e8', borderStrong: '#cdd2d8',
    text: '#15171a', text2: '#3a3f45', muted: '#5c636a',
    track: '#e3e6ea', accent: '#1f5fd1',
    warn: '#8f5400', err: '#b3261e', paused: '#8a9097', done: '#c4c9cf',
    extra: { '--side': '#eceef1', '--row-hi': '#f3f6fb' },
  },
};

export default grafito;
