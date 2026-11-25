// B · Terminal: values from docs/design/Terminal.dc.html renderVals().t.
// The design has no raised/track/onAccent: track = t.empty (the "░" color),
// raised is derived between surface and border.
import type { StyleTheme } from './types';

const terminal: StyleTheme = {
  id: 'terminal',
  font: "'JetBrains Mono', ui-monospace, Menlo, monospace",
  fontMono: "'JetBrains Mono', ui-monospace, Menlo, monospace",
  fontSize: 13,
  radius: { sm: 0, md: 0, lg: 0, pill: 0 },
  accents: ['#f0b45a', '#9a5a00', '#8fd18a', '#2f6b2a'],
  accentAsText: true,
  inks: ['#0a0a09', '#ffffff'],
  oscuro: {
    bg: '#0a0a09', surface: '#11110f', raised: '#181815',
    border: '#1f1e1b', borderStrong: '#2e2c28',
    text: '#ebe6dc', text2: '#b5afa3', muted: '#8f8a80',
    track: '#3a3833', accent: '#f0b45a',
    warn: '#d9c7a0', err: '#f2917f', paused: '#6b675e', done: '#4a4741',
  },
  claro: {
    bg: '#f4f1e9', surface: '#fbf9f4', raised: '#ebe7dc',
    border: '#e2ddd0', borderStrong: '#cfc8b8',
    text: '#1d1b17', text2: '#3d3a33', muted: '#635d52',
    track: '#d6cfbf', accent: '#9a5a00',
    warn: '#7a5c14', err: '#ad2b1b', paused: '#9b9484', done: '#bdb5a3',
  },
};

export default terminal;
