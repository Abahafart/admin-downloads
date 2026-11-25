// C · Medianoche: values from docs/design/Medianoche.dc.html renderVals().t.
// text2 and done are not in the design: derived (text2 between text and
// muted, done close to track).
import type { StyleTheme } from './types';

const medianoche: StyleTheme = {
  id: 'medianoche',
  font: "'DM Sans', system-ui, sans-serif",
  fontMono: "'JetBrains Mono', ui-monospace, Menlo, monospace",
  fontSize: 14,
  radius: { sm: 6, md: 10, lg: 14, pill: 999 },
  accents: ['#9aa1ff', '#4b52d6', '#7fd0e0', '#1d7a8c'],
  accentAsText: true,
  inks: ['#0b0d17', '#ffffff'],
  oscuro: {
    bg: '#0b0d17', surface: '#12152a', raised: '#1e2240',
    border: '#1d2140', borderStrong: '#2e3359',
    text: '#e9eaf5', text2: '#c6c9e0', muted: '#9a9fbf',
    track: '#232848', accent: '#9aa1ff',
    warn: '#e9b26a', err: '#f3958e', paused: '#5e6488', done: '#3a3f66',
  },
  claro: {
    bg: '#f4f5fb', surface: '#ffffff', raised: '#e8eaf6',
    border: '#e1e3ef', borderStrong: '#c9ccdd',
    text: '#14162b', text2: '#33375a', muted: '#575c78',
    track: '#e4e6f2', accent: '#4b52d6',
    warn: '#8a5300', err: '#b0262a', paused: '#9a9eb3', done: '#c9ccdd',
  },
};

export default medianoche;
