package web

// dashboardCSS is the whole stylesheet, inline.
//
// Split out of the document for the same reason a project splits a file: the
// UI grew from a table into an application with a sidebar, per-kind columns
// and two detail views, and one 900-line string constant is not something
// anybody can edit twice.
//
// # Everything that differs between themes is a token
//
// Not only the greys. A severity pill is dark-green-on-pale in light and
// pale-on-dark-green in dark, so hardcoding either way produces a theme where
// half the page is unreadable — which is how a "light mode" usually ships. If
// a colour appears in a rule below and is not a var(), it is a colour that
// means the same thing on both, and there are only a few.
//
// The default follows the operating system. An explicit choice is written to
// the root element as data-theme and wins, which is why every light rule
// appears twice: once under the media query guarded against an explicit dark
// choice, and once for an explicit light one.
const dashboardCSS = `
*{margin:0;padding:0;box-sizing:border-box}

:root{
  color-scheme:dark;
  --bg:#0b1120; --panel:#0f172a; --line:#1e293b; --line-soft:#172033;
  --text:#e2e8f0; --dim:#94a3b8; --faint:#64748b;
  --accent:#3b82f6; --hover:#1e293b;
  --code-bg:#060b16; --fresh:#1e3a8a;
  --pill-bg:#1e293b; --pill-fg:#cbd5e1;
  --ok-bg:#064e3b; --ok-fg:#a7f3d0; --ok:#34d399;
  --warn-bg:#422006; --warn-fg:#fde68a; --warn:#fbbf24;
  --bad-bg:#450a0a; --bad-fg:#fecaca; --bad:#f87171;
  --info-bg:#172554; --info-fg:#bfdbfe;
  --tag-bg:#1e293b; --tag-line:#27354a; --tag-fg:#94a3b8;
  --on-bg:#064e3b; --on-line:#065f46; --on-fg:#a7f3d0;
  --off-bg:#7f1d1d; --off-line:#991b1b; --off-fg:#fecaca;
  --note-warn-bg:#2a1a03; --note-warn-line:#713f12; --note-warn-fg:#fde68a;
  --note-bad-bg:#2a0a0a; --note-bad-line:#7f1d1d; --note-bad-fg:#fecaca;
  --shadow:0 1px 2px rgba(0,0,0,.4);
}

/* The light palette, written once and applied twice: for an operating system
 * that asks for it, and for somebody who asked for it here. */
@media (prefers-color-scheme: light){
  :root:not([data-theme="dark"]){
    color-scheme:light;
    --bg:#f8fafc; --panel:#ffffff; --line:#e2e8f0; --line-soft:#f1f5f9;
    --text:#0f172a; --dim:#475569; --faint:#94a3b8;
    --accent:#2563eb; --hover:#f1f5f9;
    --code-bg:#f8fafc; --fresh:#dbeafe;
    --pill-bg:#f1f5f9; --pill-fg:#475569;
    --ok-bg:#dcfce7; --ok-fg:#166534; --ok:#16a34a;
    --warn-bg:#fef3c7; --warn-fg:#92400e; --warn:#d97706;
    --bad-bg:#fee2e2; --bad-fg:#991b1b; --bad:#dc2626;
    --info-bg:#dbeafe; --info-fg:#1e40af;
    --tag-bg:#f1f5f9; --tag-line:#e2e8f0; --tag-fg:#64748b;
    --on-bg:#dcfce7; --on-line:#86efac; --on-fg:#166534;
    --off-bg:#fee2e2; --off-line:#fca5a5; --off-fg:#991b1b;
    --note-warn-bg:#fffbeb; --note-warn-line:#fcd34d; --note-warn-fg:#92400e;
    --note-bad-bg:#fef2f2; --note-bad-line:#fca5a5; --note-bad-fg:#991b1b;
    --shadow:0 1px 2px rgba(15,23,42,.06);
  }
}
:root[data-theme="light"]{
  color-scheme:light;
  --bg:#f8fafc; --panel:#ffffff; --line:#e2e8f0; --line-soft:#f1f5f9;
  --text:#0f172a; --dim:#475569; --faint:#94a3b8;
  --accent:#2563eb; --hover:#f1f5f9;
  --code-bg:#f8fafc; --fresh:#dbeafe;
  --pill-bg:#f1f5f9; --pill-fg:#475569;
  --ok-bg:#dcfce7; --ok-fg:#166534; --ok:#16a34a;
  --warn-bg:#fef3c7; --warn-fg:#92400e; --warn:#d97706;
  --bad-bg:#fee2e2; --bad-fg:#991b1b; --bad:#dc2626;
  --info-bg:#dbeafe; --info-fg:#1e40af;
  --tag-bg:#f1f5f9; --tag-line:#e2e8f0; --tag-fg:#64748b;
  --on-bg:#dcfce7; --on-line:#86efac; --on-fg:#166534;
  --off-bg:#fee2e2; --off-line:#fca5a5; --off-fg:#991b1b;
  --note-warn-bg:#fffbeb; --note-warn-line:#fcd34d; --note-warn-fg:#92400e;
  --note-bad-bg:#fef2f2; --note-bad-line:#fca5a5; --note-bad-fg:#991b1b;
  --shadow:0 1px 2px rgba(15,23,42,.06);
}

body{font:14px/1.5 -apple-system,BlinkMacSystemFont,system-ui,"Segoe UI",sans-serif;
  background:var(--bg);color:var(--text);-webkit-font-smoothing:antialiased}
code,pre,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}

/* ---- frame ------------------------------------------------------------ */
.app{display:grid;grid-template-columns:216px 1fr;min-height:100vh}
.side{border-right:1px solid var(--line);background:var(--panel);padding:16px 0;
  position:sticky;top:0;height:100vh;overflow-y:auto}
.brand{padding:0 16px 14px;display:flex;align-items:baseline;gap:8px}
.brand b{font-size:16px;font-weight:650;letter-spacing:-.01em}
.brand span{font-size:11px;color:var(--faint)}
.navgroup{font-size:10px;text-transform:uppercase;letter-spacing:.07em;color:var(--faint);
  padding:14px 16px 6px;font-weight:600}
.nav{padding:6px 16px;color:var(--dim);cursor:pointer;border-left:2px solid transparent;
  display:flex;align-items:center;gap:8px;font-size:13px;user-select:none}
.nav:hover{background:var(--hover);color:var(--text)}
.nav.on{background:var(--hover);color:var(--text);border-left-color:var(--accent);font-weight:550}
.nav .dot{width:7px;height:7px;border-radius:50%;flex:0 0 7px;background:var(--faint)}
.nav .n{margin-left:auto;font-size:11px;color:var(--faint);font-variant-numeric:tabular-nums}
.nav.on .n{color:var(--dim)}
.main{min-width:0;padding:0 22px 60px}

/* ---- sticky header ----------------------------------------------------- */
/*
 * Pinned, because the list is the one thing here that is always long: a
 * panel title you cannot see and a Live button you have to scroll back up to
 * reach are the two things a tail makes worse.
 *
 * Stretched to the edges of .main with a negative margin so its background
 * covers the full width as rows pass under it — without that the padding
 * shows a stripe of scrolling table either side.
 */
.head{position:sticky;top:0;z-index:30;background:var(--bg);
  display:flex;align-items:center;gap:10px;flex-wrap:wrap;
  margin:0 -22px;padding:16px 22px 10px}
.bar{position:sticky;top:var(--headh,54px);z-index:29;background:var(--bg);
  display:flex;gap:8px;align-items:center;flex-wrap:wrap;
  margin:0 -22px 12px;padding:0 22px 12px;border-bottom:1px solid var(--line)}
h1{font-size:19px;font-weight:650;letter-spacing:-.01em}
.sub{color:var(--faint);font-size:12px}
.spacer{margin-left:auto}
input,select,button{font:inherit;background:var(--panel);color:var(--text);
  border:1px solid var(--line);border-radius:7px;padding:5px 10px;font-size:13px}
input::placeholder{color:var(--faint)}
input:focus,select:focus{outline:none;border-color:var(--accent)}
button{cursor:pointer}
button:hover{background:var(--hover)}
button.on{background:var(--on-bg);border-color:var(--on-line);color:var(--on-fg)}
button.off{background:var(--off-bg);border-color:var(--off-line);color:var(--off-fg)}
button.icon{padding:5px 9px;line-height:1}

/* ---- table ------------------------------------------------------------ */
table{width:100%;border-collapse:collapse}
/* The column headers pin under the filter bar. A table this long with
 * scrolled-away headers is a grid of numbers nobody can name. */
th{text-align:left;font-size:10px;text-transform:uppercase;letter-spacing:.06em;
  color:var(--faint);padding:7px 10px;border-bottom:1px solid var(--line);font-weight:600;
  background:var(--bg);position:sticky;top:var(--stickyh,112px);z-index:28}
td{padding:9px 10px;border-bottom:1px solid var(--line-soft);vertical-align:top}
tr.row{cursor:pointer}
tr.row:hover td{background:var(--hover)}
tr.row td:first-child{border-left:2px solid transparent}
tr.bad td:first-child{border-left-color:var(--bad)}
tr.warn td:first-child{border-left-color:var(--warn)}
td.r{text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}
td.when{color:var(--faint);white-space:nowrap;font-size:12px}
td.num{font-variant-numeric:tabular-nums;color:var(--dim);white-space:nowrap}
.trunc{display:block;max-width:0;min-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sum{font-size:12.5px}
.err{color:var(--bad);font-size:12px;margin-top:3px}
.dim{color:var(--dim)}
.faint{color:var(--faint)}
.empty{padding:28px 10px;color:var(--faint)}

/* ---- pills ------------------------------------------------------------ */
.pill{display:inline-block;font-size:10.5px;padding:1px 7px;border-radius:999px;
  background:var(--pill-bg);color:var(--pill-fg);white-space:nowrap;font-weight:550}
.pill.ok{background:var(--ok-bg);color:var(--ok-fg)}
.pill.warn{background:var(--warn-bg);color:var(--warn-fg)}
.pill.bad{background:var(--bad-bg);color:var(--bad-fg)}
.pill.info{background:var(--info-bg);color:var(--info-fg)}
.verb{display:inline-block;min-width:46px;font-size:10.5px;font-weight:650;color:var(--dim)}
.lvl{font-size:10.5px;font-weight:650}
.lvl.e{color:var(--bad)} .lvl.w{color:var(--warn)} .lvl.i{color:var(--dim)} .lvl.d{color:var(--faint)}

/* ---- tags ------------------------------------------------------------- */
.tag{display:inline-block;font-size:10px;padding:0 6px;margin:0 3px 0 0;border-radius:4px;
  background:var(--tag-bg);border:1px solid var(--tag-line);color:var(--tag-fg);
  cursor:pointer;line-height:17px}
.tag:hover{background:var(--tag-line);color:var(--text)}
.tag.on{background:var(--accent);border-color:var(--accent);color:#ffffff}

/* ---- notices ---------------------------------------------------------- */
.note{padding:9px 12px;border-radius:7px;margin-bottom:12px;font-size:12.5px}
.note.warn{background:var(--note-warn-bg);border:1px solid var(--note-warn-line);color:var(--note-warn-fg)}
.note.bad{background:var(--note-bad-bg);border:1px solid var(--note-bad-line);color:var(--note-bad-fg)}
.note code{font-size:11.5px}

/* ---- detail ----------------------------------------------------------- */
.back{background:none;border:none;color:var(--dim);padding:0 0 10px;font-size:13px}
.back:hover{background:none;color:var(--text)}
.card{background:var(--panel);border:1px solid var(--line);border-radius:9px;
  padding:14px 16px;margin-bottom:14px;box-shadow:var(--shadow)}
.card h2{font-size:14px;font-weight:600;margin-bottom:10px;color:var(--dim)}
.card table th{background:var(--panel)}
.facts{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:10px 18px}
.fact .k{font-size:10px;text-transform:uppercase;letter-spacing:.06em;color:var(--faint)}
.fact .v{font-size:13px;margin-top:1px;word-break:break-word}
a{color:var(--accent)}
pre{background:var(--code-bg);border:1px solid var(--line);border-radius:7px;padding:12px;
  overflow-x:auto;font-size:12px;line-height:1.55;white-space:pre-wrap;word-break:break-word;
  color:var(--dim)}
.stack{max-height:420px;overflow:auto}
.off{color:var(--faint);font-variant-numeric:tabular-nums;font-size:11.5px;white-space:nowrap}
tr.fresh td{animation:fresh 1.4s ease-out}
@keyframes fresh{from{background:var(--fresh)}to{background:transparent}}
.more{display:block;width:100%;margin-top:12px;padding:8px;color:var(--dim)}

@media (max-width:860px){
  .app{grid-template-columns:1fr}
  .side{position:static;height:auto;border-right:none;border-bottom:1px solid var(--line)}
  .nav{display:inline-flex;border-left:none;border-bottom:2px solid transparent}
  .nav.on{border-left:none;border-bottom-color:var(--accent)}
  .navgroup{display:none}
  /* The header stops being sticky rather than eating half a phone screen:
   * two rows of controls pinned above a list leaves no list. */
  .head,.bar{position:static}
  th{position:static}
}
`
