# Vendored: xterm.js

Bundled (not loaded from a CDN) so TunnelTab works offline and loads nothing
from the internet. Update by replacing these files with the same files from
the new npm packages, then run the browser test (`tests/e2e`).

| File | Package | Version | License |
|---|---|---|---|
| `xterm.mjs`, `xterm.css` | `@xterm/xterm` | 6.0.0 | MIT (`LICENSE-xterm.txt`) |
| `addon-fit.mjs` | `@xterm/addon-fit` | 0.11.0 | MIT (`LICENSE-addon-fit.txt`) |
| `addon-webgl.mjs` | `@xterm/addon-webgl` | 0.19.0 | MIT (`LICENSE-addon-webgl.txt`); the trailing `sourceMappingURL` comment is removed |

Source: <https://github.com/xtermjs/xterm.js>
