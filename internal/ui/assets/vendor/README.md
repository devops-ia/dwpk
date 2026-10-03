# Vendored assets

These are committed rather than fetched because the UI's CSP is
`default-src 'self'`: a CDN `<script>` or `@import` is blocked, and so is a
`data:` URI. Anything the browser loads has to be served from this binary.

| File | Package | Version | Licence |
|---|---|---|---|
| `xterm.js` | `@xterm/xterm` | 6.0.0 | MIT (`LICENSE-xterm`) |
| `xterm.css` | `@xterm/xterm` | 6.0.0 | MIT |
| `xterm-addon-fit.js` | `@xterm/addon-fit` | 0.11.0 | MIT |
| `swagger-ui-bundle.js` | `swagger-ui-dist` | 5.29.5 | Apache-2.0 (`LICENSE-swagger-ui`) |
| `swagger-ui-standalone-preset.js` | `swagger-ui-dist` | 5.29.5 | Apache-2.0 |
| `swagger-ui.css` | `swagger-ui-dist` | 5.29.5 | Apache-2.0 |

All are the published minified UMD/bundle builds, copied unmodified except for
one line: the trailing `/*# sourceMappingURL=swagger-ui.css.map*/` is stripped
from `swagger-ui.css`. Source maps are not vendored, so leaving it in means a
404 in the console of every visitor with devtools open.
Swagger UI's own `index.html`/`swagger-initializer.js`/`oauth2-redirect.html`
are not vendored - the API docs page (`/api/docs`) supplies its own minimal
HTML instead, pointed at `/api/v1/openapi.yaml`, so nothing here depends on
Swagger UI's demo petstore default or its OAuth2 popup redirect flow, which
this API doesn't use.

## Updating

```sh
npm pack @xterm/xterm@<version> @xterm/addon-fit@<version>
tar xzf xterm-xterm-<version>.tgz
cp package/lib/xterm.js package/css/xterm.css <this directory>/
```

Take the **UMD** build from `lib/`, not the `.mjs` ESM one: the page loads it
with a plain `<script>` tag and reads the `Terminal` and `FitAddon` globals, so
an ES module would export nothing the page can see.

```sh
npm pack swagger-ui-dist@<version>
tar xzf swagger-ui-dist-<version>.tgz
cp package/swagger-ui-bundle.js package/swagger-ui-standalone-preset.js \
  package/swagger-ui.css <this directory>/
cp package/LICENSE <this directory>/LICENSE-swagger-ui
```

Then remove the `sourceMappingURL` comment from the end of `swagger-ui.css`
again; `TestAPIDocsPageAssetsAllResolve` fails if it comes back.

Update the versions in the table above in the same change, or this file starts
lying about what is actually here.
