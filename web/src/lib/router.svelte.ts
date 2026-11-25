// Hash router: #/descargas, #/linkgrabber, #/cuentas, #/red, #/ajustes.
// Real <a href> links work; styles read `router.screen`.

export type ScreenId = 'descargas' | 'linkgrabber' | 'cuentas' | 'red' | 'ajustes';

export const SCREENS: { id: ScreenId; label: string; href: string }[] = [
  { id: 'descargas', label: 'Descargas', href: '#/descargas' },
  { id: 'linkgrabber', label: 'LinkGrabber', href: '#/linkgrabber' },
  { id: 'cuentas', label: 'Cuentas', href: '#/cuentas' },
  { id: 'red', label: 'Red y rotación', href: '#/red' },
  { id: 'ajustes', label: 'Ajustes', href: '#/ajustes' },
];

function parse(hash: string): { screen: ScreenId; params: URLSearchParams } {
  const h = hash.replace(/^#\/?/, '');
  const [path, qs] = h.split('?');
  const screen = (SCREENS.find((s) => s.id === path)?.id ?? 'descargas') as ScreenId;
  return { screen, params: new URLSearchParams(qs ?? '') };
}

class Router {
  screen = $state<ScreenId>('descargas');
  params = $state<URLSearchParams>(new URLSearchParams());

  constructor() {
    if (typeof window === 'undefined') return;
    const sync = () => {
      const r = parse(window.location.hash);
      this.screen = r.screen;
      this.params = r.params;
    };
    sync();
    window.addEventListener('hashchange', sync);
  }

  go(screen: ScreenId, params?: Record<string, string>): void {
    const qs = params ? '?' + new URLSearchParams(params).toString() : '';
    const target = `#/${screen}${qs}`;
    if (typeof window !== 'undefined' && window.location.hash !== target) window.location.hash = target;
    this.screen = screen;
    this.params = new URLSearchParams(qs.slice(1));
  }
}

export const router = new Router();
