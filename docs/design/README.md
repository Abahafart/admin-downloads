# Diseños de referencia

Copia local del lienzo aprobado (https://claude.ai/artifact/ABsfSXKDarSkaPk95FJuVN): la pantalla Descargas en los 5 estilos.

- `Grafito.dc.html` (en el lienzo se llama `Main.dc.html`), `Terminal.dc.html`, `Medianoche.dc.html`, `Monocromo.dc.html`, `Enfoque.dc.html`: cada archivo contiene el diseño oscuro y el claro. Los colores de los dos temas están en el objeto `t` de `renderVals()`, y los 4 acentos en `data-props`.
- `*Claro.dc.html`: solo importan el diseño oscuro con `tema="claro"`.
- Estos archivos son referencia visual, no código de la app: cargan Google Fonts y usan `{{t.x}}` del formato del lienzo. La app usa fuentes locales y variables CSS (ver PLAN.md, sección 7.1).
