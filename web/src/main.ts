import { mount } from 'svelte';
import './theme/fonts';
import './theme/base.css';
import { installTheme } from './theme/runtime.svelte';
import { app } from './lib/store/app.svelte';
import { installShortcuts } from './lib/keyboard';
import App from './App.svelte';

// Tokens first (the boot script in index.html already set the attributes),
// then the app.
installTheme();

const mock = import.meta.env.VITE_MOCK === '1' || import.meta.env.VITE_MOCK === 'true';

async function start() {
  if (mock) {
    const { createMock } = await import('./lib/mock/server');
    const vault = (import.meta.env.VITE_MOCK_VAULT || 'locked') as NonNullable<Parameters<typeof createMock>[0]>['vault'];
    const m = createMock({ login: import.meta.env.VITE_MOCK_LOGIN === '1', vault });
    await app.start(m.fetch, { mock: true });
  } else {
    await app.start(window.fetch.bind(window));
  }
}

const target = document.getElementById('app')!;
const root = mount(App, { target });
installShortcuts(app);
void start();

export default root;
