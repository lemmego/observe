package web

// dashboardThemeJS applies a remembered theme before the first paint.
//
// In the document head and not with the rest of the script, which is the
// whole reason it is a separate constant: a theme applied after the body has
// rendered is a white flash on every navigation for somebody who chose dark,
// and that flash is the thing people notice about a theme switcher.
//
// Unremembered means "follow the operating system", so the attribute is only
// written when there is an explicit choice. localStorage is wrapped because
// it throws rather than returning null in a private window, and a dashboard
// that will not render because it could not read a colour preference would
// be a poor trade.
const dashboardThemeJS = `
(function(){
  try {
    var chosen = localStorage.getItem('observe.theme');
    if (chosen === 'light' || chosen === 'dark') {
      document.documentElement.setAttribute('data-theme', chosen);
    }
  } catch (err) { /* no storage: the operating system decides, which is the default */ }
})();
`
