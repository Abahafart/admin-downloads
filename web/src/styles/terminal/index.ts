import { defineStyle } from '../types';
import Shell from './Shell.svelte';
import DownloadsView from './DownloadsView.svelte';
import CommandBar from './CommandBar.svelte';
import Thumbnail from './Thumbnail.svelte';

export default defineStyle({
  id: 'terminal',
  nombre: 'Terminal',
  letra: 'B',
  descripcion: 'Para usuarios de la CLI: monoespaciada y con atajos a la vista',
  Shell,
  DownloadsView,
  CommandBar,
  Thumbnail,
  thumb: 'toplinks-bar',
  shortcutsVisible: true,
});
