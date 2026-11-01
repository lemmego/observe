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
.kind.exception{background:#7f1d1d;color:#fecaca}
.kind.outbound{background:#155e75;color:#a5f3fc}
.kind.notification{background:#581c87;color:#e9d5ff}
.kind.dump{background:#365314;color:#d9f99d}
.kind.view{background:#312e81;color:#c7d2fe}
.kind.gate{background:#713f12;color:#fef08a}
.kind.event{background:#134e4a;color:#99f6e4}
.kind.schedule{background:#451a03;color:#fed7aa}
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
.tag{display:inline-block;font-size:10px;padding:1px 6px;margin:1px 3px 1px 0;border-radius:4px;background:#1e293b;border:1px solid #334155;color:#94a3b8;cursor:pointer}
.tag:hover{background:#334155;color:#e2e8f0}
.tag.on{background:#1d4ed8;border-color:#1d4ed8;color:#dbeafe}
.tags{margin-top:3px}
button.live{background:#064e3b;border-color:#065f46;color:#a7f3d0}
button.paused{background:#7f1d1d;border-color:#991b1b;color:#fecaca}
pre{background:#020617;border:1px solid #1e293b;border-radius:6px;padding:12px;overflow-x:auto;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;line-height:1.5;white-space:pre-wrap;word-break:break-word}
.meta{display:flex;gap:16px;flex-wrap:wrap;color:#94a3b8;font-size:12px;margin:8px 0 12px}
.meta b{color:#e2e8f0;font-weight:600}
tr.fresh td{animation:fresh 1.2s ease-out}
@keyframes fresh{from{background:#1d4ed8}to{background:transparent}}
</style>
</head>
<body>
<h1>observe<span id="sub">recorded requests, and what each one did</span></h1>
<div id="warnings"></div>
<div class="bar">
  <select id="kind">
    <option value="">every kind</option>
    <option value="exception">exceptions</option>
    <option value="request">requests</option>
    <option value="query">queries</option>
    <option value="log">logs</option>
    <option value="cache">cache</option>
    <option value="job">jobs</option>
    <option value="mail">mail</option>
    <option value="outbound">outbound calls</option>
    <option value="notification">notifications</option>
    <option value="dump">dumps</option>
    <option value="view">views</option>
    <option value="gate">gate checks</option>
    <option value="event">events</option>
    <option value="schedule">scheduled runs</option>
  </select>
  <select id="level">
    <option value="">every level</option>
    <option value="warn">warn and above</option>
    <option value="error">errors only</option>
  </select>
  <input id="q" placeholder="contains&hellip;">
  <input id="tags" placeholder="tags, comma separated&hellip;" style="min-width:180px">
  <button id="reload">Reload</button>
  <button id="tail" class="live">Live</button>
  <button id="pause">Pause recording</button>
  <span class="stats" id="stats"></span>
</div>
<div id="active"></div>
<div id="list"></div>
<div id="detail"></div>
<script>
var state = {kind:'', level:'', q:'', tags:[], detail:null, tailing:true};
var source = null;

function esc(s){ return String(s === null || s === undefined ? '' : s)
  .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

function ms(ns){
  if (!ns) return '';
  var v = ns/1e6;
  if (v < 1) return (ns/1e3).toFixed(0)+'µs';
  if (v < 1000) return v.toFixed(1)+'ms';
  return (v/1000).toFixed(2)+'s';
}

function tags(e){
  if (!e.tags || !e.tags.length) return '';
  return '<div class="tags">'+e.tags.map(function(t){
    var on = state.tags.indexOf(t) >= 0;
    return '<span class="tag'+(on?' on':'')+'" data-tag="'+esc(t)+'">'+esc(t)+'</span>';
  }).join('')+'</div>';
}

function row(e, fresh){
  var bad = e.error || e.level >= 8;
  return '<tr class="row'+(bad?' bad':'')+(fresh?' fresh':'')+'" data-id="'+esc(e.id)+
      '" data-req="'+esc(e.request_id)+'">'+
    '<td><span class="kind '+esc(e.kind)+'">'+esc(e.kind)+'</span></td>'+
    '<td><div class="sum">'+esc(e.summary)+'</div>'+
      (e.error ? '<div class="err">'+esc(e.error)+'</div>' : '')+
      tags(e)+'</td>'+
    '<td class="dur">'+ms(e.duration)+'</td>'+
    '<td class="muted">'+esc((e.at||'').replace('T',' ').slice(0,19))+'</td>'+
  '</tr>';
}

function table(entries){
  if (!entries || !entries.length) {
    return '<p class="muted" style="padding:14px 8px">Nothing recorded yet.</p>';
  }
  return '<table><thead><tr><th>kind</th><th>what</th><th style="text-align:right">took</th><th>when</th></tr></thead><tbody>'+
    entries.map(function(e){ return row(e, false); }).join('')+'</tbody></table>';
}

function params(){
  var p = [];
  if (state.kind) p.push('kind='+encodeURIComponent(state.kind));
  if (state.level) p.push('level='+encodeURIComponent(state.level));
  if (state.q) p.push('q='+encodeURIComponent(state.q));
  if (state.tags.length) p.push('tags='+encodeURIComponent(state.tags.join(',')));
  return p.length ? '?'+p.join('&') : '';
}

function showActive(){
  var el = document.getElementById('active');
  if (!state.tags.length) { el.innerHTML = ''; return; }
  el.innerHTML = '<div style="margin-bottom:6px">filtering on '+
    state.tags.map(function(t){
      return '<span class="tag on" data-tag="'+esc(t)+'">'+esc(t)+' &times;</span>';
    }).join('')+'</div>';
  bindTags(el);
}

function load(){
  fetch('api/entries'+params())
    .then(function(r){ return r.json(); })
    .then(function(body){
      if (body.error) { document.getElementById('list').innerHTML =
        '<p class="warn">'+esc(body.error)+'</p>'; return; }
      document.getElementById('list').innerHTML = table(body.data);
      bind();
    });
}

function bindTags(root){
  var chips = root.querySelectorAll('.tag');
  for (var i = 0; i < chips.length; i++) {
    chips[i].onclick = function(ev){
      ev.stopPropagation();
      var tag = this.getAttribute('data-tag');
      var at = state.tags.indexOf(tag);
      if (at >= 0) { state.tags.splice(at, 1); } else { state.tags.push(tag); }
      showActive(); load(); retail();
    };
  }
}

function bind(){
  var rows = document.querySelectorAll('tr.row');
  for (var i = 0; i < rows.length; i++) {
    rows[i].onclick = function(){
      var req = this.getAttribute('data-req');
      if (req) { openRequest(req); } else { openEntry(this.getAttribute('data-id')); }
    };
  }
  bindTags(document.getElementById('list'));
}

// A tail prepends rather than reloading, so reading the top of the list does
// not fight with the stream arriving at it.
function prepend(e){
  if (state.detail) return;
  var body = document.querySelector('#list tbody');
  if (!body) { load(); return; }
  body.insertAdjacentHTML('afterbegin', row(e, true));
  while (body.rows.length > 200) { body.deleteRow(body.rows.length - 1); }
  bind();
}

function retail(){
  if (source) { source.close(); source = null; }
  if (!state.tailing) return;
  source = new EventSource('api/tail'+params());
  source.addEventListener('entry', function(ev){
    try { prepend(JSON.parse(ev.data)); } catch (err) { /* a frame we cannot read is not worth breaking the page over */ }
  });
  // The server says so rather than going quiet, because a stream that ends
  // silently reads as a working stream with nothing to say.
  source.addEventListener('gap', function(){
    state.tailing = false;
    paint();
    document.getElementById('warnings').innerHTML +=
      '<div class="warn">The live tail fell behind and was closed, so some '+
      'entries are not shown above. Reload to see them.</div>';
  });
}

function paint(){
  var b = document.getElementById('tail');
  b.className = state.tailing ? 'live' : '';
  b.textContent = state.tailing ? 'Live' : 'Paused view';
}

function openRequest(id){
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
      html += table((body.data || []).slice().reverse());
      detail(html);
    });
}

function openEntry(id){
  fetch('api/entries/'+encodeURIComponent(id))
    .then(function(r){ return r.json(); })
    .then(function(body){
      if (body.error) { detail('<p class="warn">'+esc(body.error)+'</p>'); return; }
      var e = body.data;
      var html = '<div class="back"><button id="close">&larr; back to the list</button></div>';
      html += '<h1 style="margin-top:6px">'+esc(e.summary)+'</h1>';
      html += '<div class="meta">'+
        '<span><b>'+esc(e.kind)+'</b> via '+esc(e.watcher)+'</span>'+
        (e.duration ? '<span>took <b>'+ms(e.duration)+'</b></span>' : '')+
        '<span>'+esc((e.at||'').replace('T',' ').slice(0,19))+'</span>'+
        (e.request_id ? '<span><a href="#" id="toreq">everything on this request &rarr;</a></span>' : '')+
      '</div>';
      if (e.tags && e.tags.length) { html += tags(e); }
      if (e.error) { html += '<pre class="err">'+esc(e.error)+'</pre>'; }
      html += '<pre>'+esc(pretty(e.data))+'</pre>';
      detail(html);
      var link = document.getElementById('toreq');
      if (link) { link.onclick = function(ev){ ev.preventDefault(); openRequest(e.request_id); }; }
    });
}

// An exception's stack and a dump's value live in the payload, and JSON
// escapes every newline in them — so they are unescaped for display rather
// than shown as one unreadable line, which is the whole reason the detail
// pane exists.
function pretty(data){
  if (data === null || data === undefined) return '(no payload)';
  try {
    var parsed = typeof data === 'string' ? JSON.parse(data) : data;
    var out = JSON.stringify(parsed, null, 2);
    return out.replace(/\\n/g, '\n').replace(/\\t/g, '    ');
  } catch (err) {
    return String(data);
  }
}

function detail(html){
  state.detail = true;
  document.getElementById('detail').innerHTML = html;
  document.getElementById('list').innerHTML = '';
  var close = document.getElementById('close');
  if (close) {
    close.onclick = function(){
      state.detail = null;
      document.getElementById('detail').innerHTML = '';
      load();
    };
  }
  bindTags(document.getElementById('detail'));
  window.scrollTo(0,0);
}

function stats(){
  fetch('api/stats').then(function(r){ return r.json(); }).then(function(body){
    var s = body.stats || {};
    var parts = [s.recorded+' recorded'];
    if (s.sampled) parts.push(s.sampled+' sampled out');
    if (s.dropped) parts.push(s.dropped+' dropped');
    if (s.failed) parts.push(s.failed+' failed to store');
    document.getElementById('stats').textContent = parts.join(' · ');

    var b = document.getElementById('pause');
    b.className = s.paused ? 'paused' : '';
    b.textContent = s.paused ? 'Recording paused' : 'Pause recording';

    var warn = '';
    // An empty table has three causes and only one is "no traffic". Saying
    // which is the difference between a useful page and a misleading one.
    if (!body.watchers || !body.watchers.length) {
      warn += '<div class="warn">No watchers are installed, so nothing is being '+
        'recorded. An empty table here does not mean your application is idle.</div>';
    } else {
      document.getElementById('sub').textContent = 'watching: '+body.watchers.join(', ');
    }
    if (s.paused) {
      warn += '<div class="warn">Recording is paused in the process that served '+
        'this page. With more than one web process the others are still '+
        'recording — this is a development convenience, not a deployment switch.</div>';
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
document.getElementById('kind').onchange = function(){ state.kind = this.value; load(); retail(); };
document.getElementById('level').onchange = function(){ state.level = this.value; load(); retail(); };
document.getElementById('q').onkeydown = function(ev){
  if (ev.key === 'Enter') { state.q = this.value; load(); retail(); }
};
document.getElementById('tags').onkeydown = function(ev){
  if (ev.key !== 'Enter') return;
  state.tags = this.value.split(',').map(function(t){ return t.trim(); })
    .filter(function(t){ return t !== ''; });
  showActive(); load(); retail();
};
document.getElementById('tail').onclick = function(){
  state.tailing = !state.tailing; paint(); retail();
  if (state.tailing) { load(); }
};
document.getElementById('pause').onclick = function(){
  var paused = document.getElementById('pause').className === 'paused';
  fetch('api/pause'+(paused ? '?resume=1' : ''), {method:'POST'})
    .then(function(){ stats(); });
};

paint();
load();
stats();
retail();
setInterval(stats, 15000);
</script>
</body>
</html>`
