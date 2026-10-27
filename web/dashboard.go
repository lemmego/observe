package web

// dashboardHTML is the whole UI: one document, inline styles, inline script.
//
// No go:embed, no bundler, no npm. The framework has no distribution path for
// frontend assets — the one package.json in the tree is marked private and
// publishes nothing — so a module that wants a UI either ships a string or
// ships nothing. tasker's queue dashboard made the same call.
//
// The script fetches relative URLs, so the dashboard works at whatever prefix
// it is mounted under without the prefix being interpolated into the markup.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>observe</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,system-ui,sans-serif;background:#0f172a;color:#e2e8f0;padding:20px}
h1{font-size:18px;font-weight:600;margin-bottom:2px}
h1 span{color:#64748b;font-weight:400;font-size:13px;margin-left:8px}
.bar{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin:16px 0}
input,select,button{font:inherit;background:#1e293b;color:#e2e8f0;border:1px solid #334155;border-radius:6px;padding:6px 10px}
input{min-width:220px}
button{cursor:pointer}
button:hover{background:#334155}
table{width:100%;border-collapse:collapse;margin-top:8px}
th{text-align:left;font-size:11px;text-transform:uppercase;letter-spacing:.04em;color:#64748b;padding:8px;border-bottom:1px solid #1e293b;font-weight:600}
td{padding:8px;border-bottom:1px solid #1e293b;vertical-align:top}
tr.row{cursor:pointer}
tr.row:hover td{background:#1e293b}
.kind{display:inline-block;font-size:11px;padding:1px 7px;border-radius:999px;background:#334155;color:#cbd5e1}
.kind.request{background:#1e3a8a;color:#bfdbfe}
.kind.query{background:#064e3b;color:#a7f3d0}
.kind.log{background:#3f3f46;color:#e4e4e7}
.kind.cache{background:#4c1d95;color:#ddd6fe}
.kind.job{background:#78350f;color:#fde68a}
.kind.mail{background:#164e63;color:#a5f3fc}
.sum{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;word-break:break-word}
.dur{color:#94a3b8;text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}
.bad .sum{color:#fca5a5}
.err{color:#fca5a5;font-size:12px;margin-top:3px;font-family:ui-monospace,monospace}
.muted{color:#64748b}
.warn{background:#422006;border:1px solid #854d0e;color:#fde68a;padding:10px 12px;border-radius:6px;margin-bottom:14px;font-size:13px}
.nplus{background:#450a0a;border:1px solid #991b1b;color:#fecaca;padding:10px 12px;border-radius:6px;margin:12px 0;font-size:13px}
.nplus code{font-family:ui-monospace,monospace;font-size:12px}
.stats{color:#64748b;font-size:12px;margin-left:auto}
#detail{margin-top:18px}
.back{margin-bottom:10px}
</style>
</head>
<body>
<h1>observe<span id="sub">recorded requests, and what each one did</span></h1>
<div id="warnings"></div>
<div class="bar">
  <select id="kind">
    <option value="">every kind</option>
    <option value="request">requests</option>
    <option value="query">queries</option>
    <option value="log">logs</option>
    <option value="cache">cache</option>
    <option value="job">jobs</option>
    <option value="mail">mail</option>
  </select>
  <select id="level">
    <option value="">every level</option>
    <option value="warn">warn and above</option>
    <option value="error">errors only</option>
  </select>
  <input id="q" placeholder="contains&hellip;">
  <button id="reload">Reload</button>
  <span class="stats" id="stats"></span>
</div>
<div id="list"></div>
<div id="detail"></div>
<script>
var state = {kind:'', level:'', q:'', detail:null};

function esc(s){ return String(s === null || s === undefined ? '' : s)
  .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

function ms(ns){
  if (!ns) return '';
  var v = ns/1e6;
  if (v < 1) return (ns/1e3).toFixed(0)+'µs';
  if (v < 1000) return v.toFixed(1)+'ms';
  return (v/1000).toFixed(2)+'s';
}

function row(e){
  var bad = e.error || e.level >= 8;
  return '<tr class="row'+(bad?' bad':'')+'" data-req="'+esc(e.request_id)+'">'+
    '<td><span class="kind '+esc(e.kind)+'">'+esc(e.kind)+'</span></td>'+
    '<td><div class="sum">'+esc(e.summary)+'</div>'+
      (e.error ? '<div class="err">'+esc(e.error)+'</div>' : '')+'</td>'+
    '<td class="dur">'+ms(e.duration)+'</td>'+
    '<td class="muted">'+esc((e.at||'').replace('T',' ').slice(0,19))+'</td>'+
  '</tr>';
}

function table(entries){
  if (!entries || !entries.length) {
    return '<p class="muted" style="padding:14px 8px">Nothing recorded yet.</p>';
  }
  return '<table><thead><tr><th>kind</th><th>what</th><th style="text-align:right">took</th><th>when</th></tr></thead><tbody>'+
    entries.map(row).join('')+'</tbody></table>';
}

function load(){
  var params = [];
  if (state.kind) params.push('kind='+encodeURIComponent(state.kind));
  if (state.level) params.push('level='+encodeURIComponent(state.level));
  if (state.q) params.push('q='+encodeURIComponent(state.q));
  fetch('api/entries'+(params.length?'?'+params.join('&'):''))
    .then(function(r){ return r.json(); })
    .then(function(body){
      if (body.error) { document.getElementById('list').innerHTML =
        '<p class="warn">'+esc(body.error)+'</p>'; return; }
      document.getElementById('list').innerHTML = table(body.data);
      bind();
    });
}

function bind(){
  var rows = document.querySelectorAll('tr.row');
  for (var i = 0; i < rows.length; i++) {
    rows[i].onclick = function(){
      var id = this.getAttribute('data-req');
      if (id) open(id);
    };
  }
}

function open(id){
  fetch('api/requests/'+encodeURIComponent(id))
    .then(function(r){ return r.json(); })
    .then(function(body){
      var html = '<div class="back"><button id="close">&larr; back to the list</button></div>';
      (body.repetitions || []).forEach(function(rep){
        html += '<div class="nplus"><strong>N+1:</strong> '+rep.count+
          ' executions of <code>'+esc(rep.normalised)+'</code> in one request, '+
          ms(rep.total)+' in total.</div>';
      });
      // Oldest first: a request reads as a sequence, where the list reads as
      // a feed.
      var entries = (body.data || []).slice().reverse();
      html += table(entries);
      document.getElementById('detail').innerHTML = html;
      document.getElementById('list').innerHTML = '';
      document.getElementById('close').onclick = function(){
        document.getElementById('detail').innerHTML = '';
        load();
      };
      window.scrollTo(0,0);
    });
}

function stats(){
  fetch('api/stats').then(function(r){ return r.json(); }).then(function(body){
    var s = body.stats || {};
    var parts = [s.recorded+' recorded'];
    if (s.dropped) parts.push(s.dropped+' dropped');
    if (s.failed) parts.push(s.failed+' failed to store');
    document.getElementById('stats').textContent = parts.join(' · ');

    var warn = '';
    // An empty table has two causes and only one is "no traffic". Saying
    // which is the difference between a useful page and a misleading one.
    if (!body.watchers || !body.watchers.length) {
      warn += '<div class="warn">No watchers are installed, so nothing is being '+
        'recorded. An empty table here does not mean your application is idle.</div>';
    } else {
      document.getElementById('sub').textContent = 'watching: '+body.watchers.join(', ');
    }
    if (s.dropped) {
      warn += '<div class="warn">'+s.dropped+' entries were dropped because the '+
        'queue filled: the store is not keeping up. Recording never blocks a '+
        'request, so this is lost detail rather than lost throughput.</div>';
    }
    document.getElementById('warnings').innerHTML = warn;
  });
}

document.getElementById('reload').onclick = function(){ load(); stats(); };
document.getElementById('kind').onchange = function(){ state.kind = this.value; load(); };
document.getElementById('level').onchange = function(){ state.level = this.value; load(); };
document.getElementById('q').onkeydown = function(ev){
  if (ev.key === 'Enter') { state.q = this.value; load(); }
};

load();
stats();
setInterval(stats, 5000);
</script>
</body>
</html>`
