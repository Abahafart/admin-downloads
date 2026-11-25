// Shared screens (Grafito language, restyled per style through tokens) and
// the resolver that picks a style's override when it has one.
import type { Component } from 'svelte';
import type { ScreenId } from '../lib/router.svelte';
import type { ScreenProps, StyleDefinition } from '../styles/types';
import LinkGrabber from './LinkGrabber.svelte';
import Accounts from './Accounts.svelte';
import Network from './Network.svelte';
import Settings from './Settings.svelte';

export const SHARED_SCREENS: Record<Exclude<ScreenId, 'descargas'>, Component<ScreenProps>> = {
  linkgrabber: LinkGrabber,
  cuentas: Accounts,
  red: Network,
  ajustes: Settings,
};

/** The component that renders `screen` in `style` (override or shared). */
export function screenFor(style: StyleDefinition, screen: ScreenId): Component<ScreenProps> {
  if (screen === 'descargas') return style.DownloadsView;
  return style.screens?.[screen] ?? SHARED_SCREENS[screen];
}
