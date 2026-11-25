// Style registry: one entry per style. To add/replace a style, create
// src/styles/<id>/index.ts exporting a StyleDefinition and reference it
// here. Data/store code never changes.
import type { EstiloId } from '../theme';
import { ESTILOS } from '../theme/types';
import type { StyleDefinition } from './types';
import grafito from './grafito';
import terminal from './terminal';
import medianoche from './medianoche';
import monocromo from './monocromo';
import enfoque from './enfoque';

export const STYLES: Record<EstiloId, StyleDefinition> = { grafito, terminal, medianoche, monocromo, enfoque };

export const STYLE_LIST: StyleDefinition[] = ESTILOS.map((id) => STYLES[id]);

export function getStyle(id: EstiloId | string | undefined): StyleDefinition {
  return STYLES[(id as EstiloId) in STYLES ? (id as EstiloId) : 'grafito'];
}
