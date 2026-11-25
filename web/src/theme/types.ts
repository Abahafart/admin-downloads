// Theme contract (PLAN.md section 7.1). Pure data: no DOM access here, so the
// files can be imported from vite.config.ts (boot script) and from tests.

export type EstiloId = 'grafito' | 'terminal' | 'medianoche' | 'monocromo' | 'enfoque';
/** User preference stored in settings.appearance.tema. */
export type Tema = 'sistema' | 'oscuro' | 'claro';
/** Resolved color scheme (data-esquema on <html>). */
export type Esquema = 'oscuro' | 'claro';

/**
 * Semantic color tokens. Every key becomes a CSS custom property
 * (camelCase -> kebab-case: borderStrong -> --border-strong, text2 -> --text-2).
 */
export interface ColorTokens {
  bg: string;
  surface: string;
  raised: string;
  border: string;
  borderStrong: string;
  text: string;
  text2: string;
  muted: string;
  /** Empty part of progress bars. */
  track: string;
  /** Default accent for this scheme (must be one of the style's accents). */
  accent: string;
  warn: string;
  err: string;
  /** Progress fill of paused downloads (non-text). */
  paused: string;
  /** Progress fill of completed downloads (non-text). */
  done: string;
}

export interface SchemeTheme extends ColorTokens {
  /**
   * Extra style-specific color tokens, e.g. {'--side': '#0b0c0e'}. Names must
   * start with "--". Listed in textOn when text is drawn on them.
   */
  extra?: Record<string, string>;
}

export interface StyleTheme {
  id: EstiloId;
  /** Font stacks; the families are bundled locally (theme/fonts.ts). */
  font: string;
  fontMono: string;
  /** Base font size in px (13 in Grafito, 14 in Monocromo...). */
  fontSize: number;
  /** Corner radius scale for shared components (0 for Terminal/Monocromo). */
  radius: { sm: number; md: number; lg: number; pill: number };
  /** The 4 accents of the design (data-props options), in design order. */
  accents: readonly string[];
  /**
   * True when the style draws text in the accent color (Terminal links,
   * Medianoche/Enfoque labels): accents then need 4.5:1 instead of 3:1.
   */
  accentAsText: boolean;
  /**
   * Candidates for --on-accent: [dark ink, light ink]. The one with the
   * higher contrast against the accent wins.
   */
  inks: readonly [string, string];
  oscuro: SchemeTheme;
  claro: SchemeTheme;
  /**
   * Extra background tokens text is drawn on (beyond bg/surface/raised),
   * checked by the contrast test, e.g. ['--side', '--row-hi'].
   */
  textOn?: readonly string[];
}

export const ESTILOS: readonly EstiloId[] = ['grafito', 'terminal', 'medianoche', 'monocromo', 'enfoque'];
export const TEMAS: readonly Tema[] = ['sistema', 'oscuro', 'claro'];
