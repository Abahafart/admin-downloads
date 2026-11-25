import { defineStyle } from '../types';
import Shell from './Shell.svelte';
import DownloadsView from './DownloadsView.svelte';
import Thumbnail from './Thumbnail.svelte';

export default defineStyle({
  id: 'monocromo',
  nombre: 'Monocromo',
  letra: 'D',
  descripcion: 'Sobrio, para pantallas OLED: sin color de estado',
  Shell,
  DownloadsView,
  Thumbnail,
  thumb: 'column-list',
});
