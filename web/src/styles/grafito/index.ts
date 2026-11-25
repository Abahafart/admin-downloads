import { defineStyle } from '../types';
import Shell from './Shell.svelte';
import DownloadsView from './DownloadsView.svelte';

export default defineStyle({
  id: 'grafito',
  nombre: 'Grafito',
  letra: 'A',
  descripcion: 'Clásico, al estilo IDM: barra lateral y tabla con carpetas desplegables',
  Shell,
  DownloadsView,
  thumb: 'sidebar',
});
