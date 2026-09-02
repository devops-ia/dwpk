// Bootstraps Swagger UI on /api/docs.
//
// This lives in a file rather than in a <script> block on the page because the
// application's Content-Security-Policy is `default-src 'self'` with no
// script-src exemption: an inline script is refused outright, which is what
// left the page blank. Loosening the CSP for one page would have been the
// larger change and the worse one.
//
// The spec URL is read from a data attribute rather than interpolated, since a
// static file cannot know the base path the server is mounted under.
(function () {
  'use strict';

  window.addEventListener('load', function () {
    var mount = document.getElementById('swagger-ui');
    if (!mount) {
      return;
    }
    window.ui = SwaggerUIBundle({
      url: mount.dataset.specUrl,
      dom_id: '#swagger-ui',
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
      layout: 'StandaloneLayout'
    });
  });
})();
