// No-flash boot script (PLAN.md 7.1 "Sin parpadeo al arrancar"). It is a
// plain ES5 string so it can be inlined in index.html by the Vite plugin in
// vite.config.ts and executed before Svelte (and before first paint). It
// reads the appearance mirrored in localStorage (wrapped in try/catch),
// resolves "sistema" with prefers-color-scheme and sets data-estilo,
// data-tema, data-esquema, data-acento, --accent, --on-accent and the page
// background on <html>. T is bootTable() from theme/index.ts.

export const STORAGE_KEY = 'adesc.appearance';

export const BOOT_JS = `function(T){
var d=document.documentElement,p={};
try{p=JSON.parse(localStorage.getItem(${JSON.stringify(STORAGE_KEY)})||'{}')||{};}catch(e){p={};}
var est=(p.estilo&&T.styles.hasOwnProperty(p.estilo))?p.estilo:T.def;
var tema=(p.tema==='oscuro'||p.tema==='claro')?p.tema:'sistema';
var esq=tema;
if(tema==='sistema'){esq='oscuro';try{if(window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches){esq='claro';}}catch(e){}}
var s=T.styles[est][esq],a=String(p.acento||'').toLowerCase(),ink=s.acc.hasOwnProperty(a)?s.acc[a]:'';
if(!ink){a=s.defAcc;ink=s.defInk;}
d.setAttribute('data-estilo',est);d.setAttribute('data-tema',tema);d.setAttribute('data-esquema',esq);d.setAttribute('data-acento',a);
d.style.setProperty('--accent',a);d.style.setProperty('--on-accent',ink);
d.style.backgroundColor=s.bg;d.style.color=s.text;d.style.colorScheme=esq==='oscuro'?'dark':'light';
}`;

/** The full inline <script> body: BOOT_JS applied to the table. */
export function bootScript(table: unknown): string {
  return `(${BOOT_JS})(${JSON.stringify(table)});`;
}
