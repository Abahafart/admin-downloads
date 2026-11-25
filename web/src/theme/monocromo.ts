// D · Monocromo: values from docs/design/Monocromo.dc.html renderVals().t
// (line -> border, line2 -> borderStrong). The design has no state colors:
// warn/err equal the text color (states differ by text, italics, weight and
// strike-through), paused = muted, done/track = line2.
import type { StyleTheme } from './types';

const monocromo: StyleTheme = {
  id: 'monocromo',
  font: "'Geist', 'Helvetica Neue', Arial, sans-serif",
  fontMono: "'Geist Mono', ui-monospace, monospace",
  fontSize: 14,
  radius: { sm: 0, md: 0, lg: 0, pill: 0 },
  accents: ['#ffffff', '#0a0a0a', '#ff5a36', '#c6ff3d'],
  accentAsText: false,
  inks: ['#000000', '#ffffff'],
  oscuro: {
    bg: '#000000', surface: '#000000', raised: '#111111',
    border: '#1c1c1c', borderStrong: '#3d3d3d',
    text: '#f5f5f5', text2: '#d0d0d0', muted: '#9b9b9b',
    track: '#3d3d3d', accent: '#ffffff',
    warn: '#f5f5f5', err: '#f5f5f5', paused: '#9b9b9b', done: '#3d3d3d',
  },
  claro: {
    bg: '#ffffff', surface: '#ffffff', raised: '#f2f2f2',
    border: '#e8e8e8', borderStrong: '#bdbdbd',
    text: '#0a0a0a', text2: '#2e2e2e', muted: '#5e5e5e',
    track: '#bdbdbd', accent: '#0a0a0a',
    warn: '#0a0a0a', err: '#0a0a0a', paused: '#5e5e5e', done: '#bdbdbd',
  },
};

export default monocromo;
