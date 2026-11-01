package web

// dashboardHTML is the document: a sidebar, a header and two panes the
// script fills.
//
// No go:embed, no bundler, no npm. The framework has no distribution path
// for frontend assets — the one package.json in the tree is marked private
// and publishes nothing — so a module that wants a UI either ships a string
// or ships nothing. tasker's queue dashboard made the same call.
//
// Everything is fetched from relative URLs, so the dashboard works at
// whatever prefix it is mounted under without the prefix being interpolated
// into the markup.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>observe</title>
<style>` + dashboardCSS + `</style>
<script>` + dashboardThemeJS + `</script>
</head>
<body>
<div class="app">
  <nav class="side">
    <div class="brand"><b>observe</b><span id="watching"></span></div>
    <div id="nav"></div>
  </nav>
  <main class="main">
    <div class="head" id="head">
      <h1 id="title">Requests</h1>
      <span class="sub" id="subtitle"></span>
      <span class="spacer"></span>
      <button id="tail" class="on" title="Stream entries as they are recorded">Live</button>
      <button id="pause" title="Stop this process recording">Pause</button>
      <button id="clear" title="Delete every recorded entry">Clear</button>
      <button id="theme" class="icon" title="Switch between light and dark">&#9681;</button>
    </div>
    <div class="bar">
      <input id="q" placeholder="contains&hellip;" style="min-width:240px">
      <input id="tags" placeholder="tags, comma separated&hellip;" style="min-width:190px">
      <input id="source" placeholder="host or instance&hellip;" style="min-width:150px">
      <select id="level">
        <option value="">every level</option>
        <option value="warn">warn and above</option>
        <option value="error">errors only</option>
      </select>
      <button id="reload">Reload</button>
      <span class="spacer"></span>
      <span class="sub" id="stats"></span>
    </div>
    <div id="notices"></div>
    <div id="chips"></div>
    <div id="list"></div>
    <div id="detail"></div>
  </main>
</div>
<script>` + dashboardJS + `</script>
</body>
</html>`
