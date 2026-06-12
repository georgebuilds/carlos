/* Matomo — privacy-friendly analytics for analytics.georgebuilds.com (site id 7).
   Loaded once per page; the actual tracking config lives only here. */
(function () {
  var _paq = (window._paq = window._paq || []);
  /* tracker methods like "setCustomDimension" should be called before "trackPageView" */
  _paq.push(['trackPageView']);
  _paq.push(['enableLinkTracking']);
  var u = '//analytics.georgebuilds.com/';
  _paq.push(['setTrackerUrl', u + 'matomo.php']);
  _paq.push(['setSiteId', '7']);
  var d = document,
    g = d.createElement('script'),
    s = d.getElementsByTagName('script')[0];
  g.async = true;
  g.src = u + 'matomo.js';
  s.parentNode.insertBefore(g, s);
})();
