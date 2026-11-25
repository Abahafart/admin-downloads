// The style contract. A style = one folder under src/styles/<id>/ exporting
// a StyleDefinition, plus one entry in registry.ts. Styles never touch the
// API or the store internals: they read `app` (lib/store/app.svelte.ts) and
// call its actions; tokens come from src/theme/<id>.ts.
import type { Component, Snippet } from 'svelte';
import type { Esquema, EstiloId } from '../theme';
import type { ScreenId } from '../lib/router.svelte';

export interface ShellProps {
  /** Current screen (also in router.screen). */
  screen: ScreenId;
  /** The current screen's content; render it inside the shell's <main>. */
  children: Snippet;
}

/** Props of any screen component (Descargas and the others): none today. */
export type ScreenProps = Record<string, never>;

/** Layouts of the built-in thumbnail drawn in Ajustes › Apariencia. */
export type ThumbLayout = 'sidebar' | 'toplinks-bar' | 'tabs-cards' | 'column-list' | 'toplinks-focus';

export interface StyleDefinition {
  id: EstiloId;
  /** Display name: "Grafito". */
  nombre: string;
  /** Letter in the design canvas: "A". */
  letra: string;
  /** One line for the style picker. */
  descripcion: string;
  /** Navigation + fixed status (speed, exit, IP, clipboard) + <main>. */
  Shell: Component<ShellProps>;
  /** The Descargas screen. */
  DownloadsView: Component<ScreenProps>;
  /**
   * Optional overrides for the other screens; missing ones use the shared
   * screens in src/screens/ (restyled through tokens and [data-estilo] CSS).
   */
  screens?: Partial<Record<Exclude<ScreenId, 'descargas'>, Component<ScreenProps>>>;
  /** Optional custom thumbnail; otherwise StyleThumb draws `thumb`. */
  Thumbnail?: Component<{ esquema: Esquema }>;
  thumb: ThumbLayout;
  /**
   * True when the shell always shows the shortcuts (Terminal's bottom bar);
   * the others show the "?" help hint.
   */
  shortcutsVisible?: boolean;
  /** Optional replacements of the shared overlays. */
  CommandBar?: Component;
  HelpOverlay?: Component;
}

export function defineStyle(s: StyleDefinition): StyleDefinition {
  return s;
}
