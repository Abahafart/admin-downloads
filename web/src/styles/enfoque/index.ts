import { defineStyle } from '../types';
import Shell from './Shell.svelte';
import DownloadsView from './DownloadsView.svelte';
import Thumbnail from './Thumbnail.svelte';

export default defineStyle({
  id: 'enfoque',
  nombre: 'Enfoque',
  letra: 'E',
  descripcion: 'Calmado: la descarga activa en grande',
  Shell,
  DownloadsView,
  Thumbnail,
  thumb: 'toplinks-focus',
});
