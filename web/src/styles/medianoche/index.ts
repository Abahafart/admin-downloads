import { defineStyle } from '../types';
import Shell from './Shell.svelte';
import DownloadsView from './DownloadsView.svelte';
import Thumbnail from './Thumbnail.svelte';

export default defineStyle({
  id: 'medianoche',
  nombre: 'Medianoche',
  letra: 'C',
  descripcion: 'Amigable: tarjetas y errores explicados en una frase',
  Shell,
  DownloadsView,
  Thumbnail,
  thumb: 'tabs-cards',
});
