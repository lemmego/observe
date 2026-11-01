package web

// dashboardJS is the dashboard's behaviour: panel selection, per-kind
// columns, the two detail views, the live tail and the filters.
//
// Plain ES5-ish JavaScript with no framework and no build step, for the
// reason given on dashboardHTML. It is in its own file because it is the
// part that changes.
//
// The file is ui_script.go and not ui_js.go, which is not a style
// preference: "js" is a GOOS, so Go applies an implicit build constraint to
// any file ending _js.go and excludes it from every build that is not
// WebAssembly. The symptom is "undefined: dashboardJS" against a file that
// is plainly there and parses cleanly.
const dashboardJS = `
/* observe dashboard.
 *
 * One pane at a time, chosen from the sidebar. That is the organising idea
 * and it is the one the old single-table view was missing: an undifferentiated
 * feed buries a 500 under the fourteen near-identical queries of the request
 * before it, and the thing you came to find is the thing you scroll past.
 *
 * Each panel declares its own columns, because a query row and a request row
 * answer different questions and giving them the same three columns serves
 * neither.
 */

var PANELS = [
  {kind:'request',      title:'Requests',      group:'HTTP'},
  {kind:'exception',    title:'Exceptions',    group:'HTTP'},
  {kind:'view',         title:'Views',         group:'HTTP'},
  {kind:'outbound',     title:'Outgoing HTTP', group:'HTTP'},
  {kind:'query',        title:'Queries',       group:'Data'},
  {kind:'cache',        title:'Cache',         group:'Data'},
  {kind:'job',          title:'Jobs',          group:'Background'},
  {kind:'schedule',     title:'Schedule',      group:'Background'},
  {kind:'mail',         title:'Mail',          group:'Background'},
  {kind:'notification', title:'Notifications', group:'Background'},
  {kind:'log',          title:'Logs',          group:'Other'},
  {kind:'gate',         title:'Gates',         group:'Other'},
  {kind:'event',        title:'Events',        group:'Other'},
  {kind:'dump',         title:'Dumps',         group:'Other'},
  {kind:'',             title:'Everything',    group:'Other'}
];

var state = {panel:'request', q:'', level:'', tags:[], cursor:null, rows:[], view:null, tailing:true};
var source = null;

/* ---- helpers --------------------------------------------------------- */

function esc(s){ return String(s === null || s === undefined ? '' : s)
  .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

function el(id){ return document.getElementById(id); }

function ms(ns){
  if (!ns) return '';
  var v = ns/1e6;
  if (v < 1) return (ns/1e3).toFixed(0)+'µs';
  if (v < 1000) return v.toFixed(1)+'ms';
  return (v/1000).toFixed(2)+'s';
}

/* Relative, because the question is always "how long ago" and never "at what
 * o'clock". The exact time is in the title attribute for the one time in
 * twenty that it matters. */
function ago(iso){
  var then = Date.parse(iso);
  if (isNaN(then)) return '';
  var s = Math.max(0, (Date.now() - then)/1000);
  if (s < 10) return 'just now';
  if (s < 60) return Math.floor(s)+'s ago';
  if (s < 3600) return Math.floor(s/60)+'m ago';
  if (s < 86400) return Math.floor(s/3600)+'h ago';
  return Math.floor(s/86400)+'d ago';
}

function when(e){
  return '<td class="when" title="'+esc(e.at)+'">'+esc(ago(e.at))+'</td>';
}

function data(e){
  if (!e.data) return {};
  if (typeof e.data === 'object') return e.data;
  try { return JSON.parse(e.data); } catch (err) { return {}; }
}

function statusPill(code){
  if (!code) return '';
  var cls = 'ok';
  if (code >= 500) { cls = 'bad'; } else if (code >= 400) { cls = 'warn'; }
  else if (code >= 300) { cls = 'info'; }
  return '<span class="pill '+cls+'">'+code+'</span>';
}

/* slog.Level marshals itself as a string — "INFO", "ERROR", and "ERROR+2"
 * for a level between the named ones — so every numeric comparison against
 * e.level is a comparison with NaN and quietly false. That is why nothing on
 * this page was ever coloured by severity: a 500 looked exactly like a 200
 * unless it also carried an error string.
 *
 * Both forms are handled, because Entry.Level is a slog.Level in Go and a
 * number is what a hand-written store or a future encoding would produce. */
var LEVELS = {DEBUG:-4, INFO:0, WARN:4, ERROR:8};

function levelOf(level){
  if (typeof level === 'number') return level;
  if (typeof level !== 'string' || level === '') return 0;
  var sign = level.indexOf('+') > 0 ? 1 : (level.indexOf('-') > 0 ? -1 : 0);
  var name = sign ? level.slice(0, level.search(/[+-]/)) : level;
  var offset = sign ? parseInt(level.slice(level.search(/[+-]/)+1), 10) || 0 : 0;
  var base = LEVELS[name.toUpperCase()];
  if (base === undefined) return 0;
  return base + sign*offset;
}

function levelName(level){
  var n = levelOf(level);
  if (n >= 8) return ['ERROR','e'];
  if (n >= 4) return ['WARN','w'];
  if (n >= 0) return ['INFO','i'];
  return ['DEBUG','d'];
}

function rowClass(e){
  var n = levelOf(e.level);
  if (e.error || n >= 8) return 'bad';
  if (n >= 4) return 'warn';
  return '';
}

function trunc(text){ return '<span class="trunc mono sum">'+esc(text)+'</span>'; }

/* ---- per-kind columns ------------------------------------------------ */
/*
 * Each entry kind gets the columns that answer its own question. A request
 * wants verb, path and status; a query wants the statement and how long it
 * took; a gate wants who was refused what. The old view gave all of them
 * kind/summary/duration and so answered none of them well.
 */
var COLUMNS = {
  request: {
    head: ['', 'Path', 'Status', 'Duration', 'When'],
    cells: function(e){
      var d = data(e);
      var path = d.path || e.summary;
      return '<td><span class="verb mono">'+esc(d.method || '')+'</span></td>'+
        '<td>'+trunc(path)+tagsOf(e)+'</td>'+
        '<td>'+statusPill(d.status)+'</td>'+
        '<td class="r dim">'+ms(e.duration)+'</td>'+ when(e);
    }
  },
  query: {
    head: ['Statement', 'Rows', 'Duration', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(e.summary)+'</td>'+
        '<td class="num">'+(d.rows === undefined ? '' : d.rows)+'</td>'+
        '<td class="r dim">'+ms(e.duration)+'</td>'+ when(e);
    }
  },
  exception: {
    head: ['Message', 'When'],
    cells: function(e){
      return '<td>'+trunc(e.summary)+'</td>'+ when(e);
    }
  },
  log: {
    head: ['Level', 'Message', 'When'],
    cells: function(e){
      var l = levelName(e.level);
      return '<td><span class="lvl '+l[1]+'">'+l[0]+'</span></td>'+
        '<td>'+trunc(e.summary)+(e.error ? '<div class="err mono">'+esc(e.error)+'</div>' : '')+'</td>'+
        when(e);
    }
  },
  cache: {
    head: ['', 'Key', 'When'],
    cells: function(e){
      var d = data(e);
      var op = d.op || '';
      var cls = op === 'hit' ? 'ok' : (op === 'miss' ? 'warn' : '');
      return '<td><span class="pill '+cls+'">'+esc(op)+'</span></td>'+
        '<td>'+trunc(d.key || e.summary)+'</td>'+ when(e);
    }
  },
  job: {
    head: ['', 'Job', 'Queue', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td><span class="pill '+(d.op === 'queued' ? 'info' : '')+'">'+esc(d.op || '')+'</span></td>'+
        '<td>'+trunc(d.kind || e.summary)+'</td>'+
        '<td class="dim">'+esc(d.queue || '')+'</td>'+ when(e);
    }
  },
  schedule: {
    head: ['Entry', 'Schedule', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(d.id || e.summary)+'</td>'+
        '<td class="mono dim">'+esc(d.schedule || '')+'</td>'+ when(e);
    }
  },
  view: {
    head: ['View', 'Duration', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(d.view || e.summary)+'</td>'+
        '<td class="r dim">'+ms(e.duration)+'</td>'+ when(e);
    }
  },
  gate: {
    head: ['', 'Ability', 'Subject', 'When'],
    cells: function(e){
      var d = data(e);
      var allowed = d.allowed === true;
      return '<td><span class="pill '+(allowed ? 'ok' : 'warn')+'">'+
          (allowed ? 'allowed' : 'denied')+'</span></td>'+
        '<td>'+trunc(d.ability || e.summary)+
          (d.reason ? '<div class="faint" style="font-size:11.5px">'+esc(d.reason)+'</div>' : '')+'</td>'+
        '<td class="dim mono">'+esc(d.anonymous ? 'anonymous' : (d.subject_id ? 'user '+d.subject_id : ''))+'</td>'+
        when(e);
    }
  },
  outbound: {
    head: ['', 'URL', 'Status', 'Duration', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td><span class="verb mono">'+esc(d.method || '')+'</span></td>'+
        '<td>'+trunc(d.url || e.summary)+'</td>'+
        '<td>'+statusPill(d.status)+'</td>'+
        '<td class="r dim">'+ms(e.duration)+'</td>'+ when(e);
    }
  },
  notification: {
    head: ['Notification', 'Channel', 'Recipient', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(d.notification || e.summary)+'</td>'+
        '<td class="dim">'+esc(d.channel || '')+'</td>'+
        '<td class="dim mono">'+esc(d.recipient_id ? d.recipient_kind+' '+d.recipient_id : '')+'</td>'+
        when(e);
    }
  },
  mail: {
    head: ['Subject', 'To', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(d.subject || e.summary)+'</td>'+
        '<td class="dim mono">'+esc((d.to || []).join(', '))+'</td>'+ when(e);
    }
  },
  event: {
    head: ['Event', 'Payload', 'When'],
    cells: function(e){
      var d = data(e);
      return '<td>'+trunc(d.event || e.summary)+'</td>'+
        '<td class="dim mono">'+esc(d.payload_type || '')+'</td>'+ when(e);
    }
  },
  dump: {
    head: ['Label', 'When'],
    cells: function(e){ return '<td>'+trunc(e.summary)+'</td>'+ when(e); }
  }
};

/* The fallback, and what "Everything" uses: a kind column, because a mixed
 * list without one is unreadable. */
var ANY = {
  head: ['Kind', 'What', 'Duration', 'When'],
  cells: function(e){
    return '<td><span class="pill">'+esc(e.kind)+'</span></td>'+
      '<td>'+trunc(e.summary)+(e.error ? '<div class="err mono">'+esc(e.error)+'</div>' : '')+'</td>'+
      '<td class="r dim">'+ms(e.duration)+'</td>'+ when(e);
  }
};

function columnsFor(kind){ return (kind && COLUMNS[kind]) || ANY; }

/* Tags a column already shows are dropped rather than repeated. A
 * method:GET chip under a GET column and a status:200 chip beside a 200 pill
 * are noise, and noise on every row is what made the old list hard to read.
 * They are still filterable by typing them. */
var REDUNDANT = {request:['method:', 'status:']};

function tagsOf(e, kind){
  if (!e.tags || !e.tags.length) return '';
  var hide = REDUNDANT[kind === undefined ? e.kind : kind] || [];
  var shown = e.tags.filter(function(t){
    for (var i = 0; i < hide.length; i++) { if (t.indexOf(hide[i]) === 0) return false; }
    return true;
  });
  if (!shown.length) return '';
  return '<div style="margin-top:3px">'+shown.map(function(t){
    return '<span class="tag'+(state.tags.indexOf(t) >= 0 ? ' on' : '')+
      '" data-tag="'+esc(t)+'">'+esc(t)+'</span>';
  }).join('')+'</div>';
}

/* ---- list ------------------------------------------------------------ */

function panel(){
  for (var i = 0; i < PANELS.length; i++) {
    if (PANELS[i].kind === state.panel) return PANELS[i];
  }
  return PANELS[PANELS.length-1];
}

function params(extra){
  var p = [];
  if (state.panel) p.push('kind='+encodeURIComponent(state.panel));
  if (state.level) p.push('level='+encodeURIComponent(state.level));
  if (state.q) p.push('q='+encodeURIComponent(state.q));
  if (state.tags.length) p.push('tags='+encodeURIComponent(state.tags.join(',')));
  if (extra) p.push(extra);
  return p.length ? '?'+p.join('&') : '';
}

function renderList(){
  var cols = columnsFor(state.panel);
  if (!state.rows.length) {
    el('list').innerHTML = '<p class="empty">Nothing recorded here yet.</p>';
    return;
  }
  var html = '<table><thead><tr>';
  for (var i = 0; i < cols.head.length; i++) {
    var last = i === cols.head.length-1;
    html += '<th'+(cols.head[i] === 'Duration' ? ' style="text-align:right"' : '')+'>'+
      esc(cols.head[i])+'</th>';
  }
  html += '</tr></thead><tbody>'+state.rows.map(function(e){
    return '<tr class="row '+rowClass(e)+(e._fresh ? ' fresh' : '')+
      '" data-id="'+esc(e.id)+'" data-req="'+esc(e.request_id || '')+'">'+cols.cells(e)+'</tr>';
  }).join('')+'</tbody></table>';
  if (state.cursor) {
    html += '<button class="more" id="more">Load more</button>';
  }
  el('list').innerHTML = html;
  bindRows();
}

function load(append){
  var url = 'api/entries'+params(append && state.cursor ? 'cursor='+encodeURIComponent(state.cursor) : '');
  fetch(url).then(function(r){ return r.json(); }).then(function(body){
    if (body.error) { el('list').innerHTML = '<p class="note bad">'+esc(body.error)+'</p>'; return; }
    var rows = body.data || [];
    state.rows = append ? state.rows.concat(rows) : rows;
    /* A cursor with nothing behind it is a Load more that loads nothing, so
     * a short page ends the list rather than offering another click. */
    state.cursor = rows.length && body.cursor ? body.cursor : null;
    renderList();
  });
}

function bindRows(){
  var rows = el('list').querySelectorAll('tr.row');
  for (var i = 0; i < rows.length; i++) {
    rows[i].onclick = function(ev){
      if (ev.target.className.indexOf('tag') >= 0) return;
      var req = this.getAttribute('data-req');
      var id = this.getAttribute('data-id');
      /* A request opens its own story; anything else opens itself, with a
       * link to the request it belongs to. */
      if (this.querySelector('td') && state.panel === 'request' && req) { openRequest(req); }
      else { openEntry(id); }
    };
  }
  bindTags(el('list'));
  var more = el('more');
  if (more) { more.onclick = function(){ load(true); }; }
}

function bindTags(root){
  var chips = root.querySelectorAll('.tag');
  for (var i = 0; i < chips.length; i++) {
    chips[i].onclick = function(ev){
      ev.stopPropagation();
      var tag = this.getAttribute('data-tag');
      var at = state.tags.indexOf(tag);
      if (at >= 0) { state.tags.splice(at, 1); } else { state.tags.push(tag); }
      el('tags').value = state.tags.join(',');
      showChips(); refresh();
    };
  }
}

function showChips(){
  if (!state.tags.length) { el('chips').innerHTML = ''; return; }
  el('chips').innerHTML = '<div style="margin-bottom:8px" class="sub">filtering on '+
    state.tags.map(function(t){
      return '<span class="tag on" data-tag="'+esc(t)+'">'+esc(t)+' &times;</span>';
    }).join('')+'</div>';
  bindTags(el('chips'));
}

/* ---- detail ---------------------------------------------------------- */

function fact(k, v){
  if (v === '' || v === undefined || v === null) return '';
  return '<div class="fact"><div class="k">'+esc(k)+'</div><div class="v">'+v+'</div></div>';
}

function pretty(d){
  if (d === null || d === undefined || d === '') return '(no payload)';
  try {
    var parsed = typeof d === 'string' ? JSON.parse(d) : d;
    return JSON.stringify(parsed, null, 2).replace(/\\n/g, '\n').replace(/\\t/g, '    ');
  } catch (err) { return String(d); }
}

function detail(html){
  state.view = 'detail';
  el('detail').innerHTML = '<button class="back" id="back">&larr; back to '+
    esc(panel().title.toLowerCase())+'</button>'+html;
  el('list').innerHTML = '';
  el('back').onclick = function(){ state.view = null; el('detail').innerHTML = ''; load(false); };
  bindTags(el('detail'));
  toTop();
}

/* The main pane is the scroll container on a desktop and the document is on
 * a phone, so both are reset: scrolling only the window would leave a detail
 * view opened halfway down. */
function toTop(){
  var main = document.querySelector('.main');
  if (main) { main.scrollTop = 0; }
  window.scrollTo(0,0);
}

function openEntry(id){
  fetch('api/entries/'+encodeURIComponent(id))
    .then(function(r){ return r.json(); })
    .then(function(body){
      if (body.error) { detail('<p class="note bad">'+esc(body.error)+'</p>'); return; }
      var e = body.data, d = data(e);
      var l = levelName(e.level);
      var html = '<h1 style="margin-bottom:10px">'+esc(e.summary)+'</h1>';
      html += '<div class="card"><div class="facts">'+
        fact('kind', '<span class="pill">'+esc(e.kind)+'</span>')+
        fact('level', '<span class="lvl '+l[1]+'">'+l[0]+'</span>')+
        fact('watcher', esc(e.watcher))+
        fact('took', e.duration ? ms(e.duration) : '')+
        fact('when', esc(ago(e.at))+' <span class="faint">('+esc(e.at)+')</span>')+
        (e.request_id ? fact('request',
          '<a href="#" id="toreq" class="mono">'+esc(e.request_id.slice(0,12))+'&hellip; &rarr;</a>') : '')+
      '</div>'+(e.tags && e.tags.length ? '<div style="margin-top:12px">'+tagsOf(e, '')+'</div>' : '')+'</div>';

      if (e.error) { html += '<div class="card"><h2>Error</h2><pre>'+esc(e.error)+'</pre></div>'; }
      /* A panic's stack is the reason the detail view exists, and JSON
       * escapes every newline in it — so it is unescaped and given its own
       * scrolling block rather than shown as one unreadable line. */
      if (d.stack) {
        html += '<div class="card"><h2>Stack</h2><pre class="stack">'+
          esc(String(d.stack).replace(/\\n/g, '\n').replace(/\\t/g, '    '))+'</pre></div>';
      }
      html += '<div class="card"><h2>Payload</h2><pre>'+esc(pretty(e.data))+'</pre></div>';
      detail(html);
      var link = el('toreq');
      if (link) { link.onclick = function(ev){ ev.preventDefault(); openRequest(e.request_id); }; }
    });
}

function openRequest(id){
  fetch('api/requests/'+encodeURIComponent(id))
    .then(function(r){ return r.json(); })
    .then(function(body){
      var entries = (body.data || []).slice().reverse();  /* oldest first: a request is a sequence */
      var req = null;
      for (var i = 0; i < entries.length; i++) {
        if (entries[i].kind === 'request') { req = entries[i]; break; }
      }

      var html = '';
      /* The header the old view did not have at all: you clicked a request
       * and got a wall of its queries with nothing saying which request. */
      if (req) {
        var d = data(req);
        html += '<h1 style="margin-bottom:10px"><span class="mono">'+esc(d.method || '')+'</span> '+
          esc(d.path || req.summary)+'</h1>';
        html += '<div class="card"><div class="facts">'+
          fact('status', statusPill(d.status))+
          fact('took', ms(req.duration))+
          fact('when', esc(ago(req.at))+' <span class="faint">('+esc(req.at)+')</span>')+
          fact('ip', esc(d.ip || ''))+
          fact('bytes', d.bytes ? String(d.bytes) : '')+
          fact('entries', String(entries.length - 1))+
        '</div>'+(req.tags && req.tags.length ? '<div style="margin-top:12px">'+tagsOf(req, '')+'</div>' : '')+
        '</div>';
      }

      (body.repetitions || []).forEach(function(rep){
        html += '<div class="note bad"><b>N+1:</b> '+rep.count+
          ' executions of <code>'+esc(rep.normalised)+'</code>, '+ms(rep.total)+' in total.</div>';
      });

      /* Offsets from the start of the request rather than wall-clock times.
       * Fourteen identical timestamps say nothing; "+12ms" says where in the
       * request the slow thing was. */
      var start = req ? Date.parse(req.at) : (entries.length ? Date.parse(entries[0].at) : 0);
      html += '<div class="card" style="padding:0"><table><thead><tr>'+
        '<th style="width:62px">At</th><th>Kind</th><th>What</th>'+
        '<th style="text-align:right">Took</th></tr></thead><tbody>';
      entries.forEach(function(e){
        if (e === req) return;
        var offset = Math.max(0, Date.parse(e.at) - start);
        html += '<tr class="row '+rowClass(e)+'" data-id="'+esc(e.id)+'">'+
          '<td class="off">+'+offset+'ms</td>'+
          '<td><span class="pill">'+esc(e.kind)+'</span></td>'+
          '<td>'+trunc(e.summary)+
            (e.error ? '<div class="err mono">'+esc(e.error)+'</div>' : '')+'</td>'+
          '<td class="r dim">'+ms(e.duration)+'</td></tr>';
      });
      html += '</tbody></table></div>';

      detail(html);
      var rows = el('detail').querySelectorAll('tr.row');
      for (var i = 0; i < rows.length; i++) {
        rows[i].onclick = function(){ openEntry(this.getAttribute('data-id')); };
      }
    });
}

/* ---- sidebar, stats, tail -------------------------------------------- */

function renderNav(counts){
  var html = '', group = '';
  PANELS.forEach(function(p){
    if (p.group !== group) { group = p.group; html += '<div class="navgroup">'+esc(group)+'</div>'; }
    var n = p.kind ? (counts[p.kind] || 0) : total(counts);
    html += '<div class="nav'+(p.kind === state.panel ? ' on' : '')+'" data-kind="'+esc(p.kind)+'">'+
      '<span class="dot" style="background:'+dot(p.kind)+'"></span>'+esc(p.title)+
      '<span class="n">'+(n || '')+'</span></div>';
  });
  el('nav').innerHTML = html;
  var items = el('nav').querySelectorAll('.nav');
  for (var i = 0; i < items.length; i++) {
    items[i].onclick = function(){
      state.panel = this.getAttribute('data-kind');
      state.view = null;
      el('detail').innerHTML = '';
      el('title').textContent = panel().title;
      refresh();
    };
  }
}

function total(counts){
  var n = 0;
  for (var k in counts) { if (Object.prototype.hasOwnProperty.call(counts, k)) { n += counts[k]; } }
  return n;
}

var DOTS = {request:'#3b82f6', query:'#10b981', log:'#64748b', cache:'#8b5cf6',
  job:'#f59e0b', mail:'#06b6d4', exception:'#ef4444', outbound:'#22d3ee',
  notification:'#a855f7', dump:'#84cc16', view:'#6366f1', gate:'#eab308',
  event:'#14b8a6', schedule:'#fb923c'};
function dot(kind){ return DOTS[kind] || '#475569'; }

function stats(){
  fetch('api/stats').then(function(r){ return r.json(); }).then(function(body){
    var s = body.stats || {};
    var parts = [s.recorded+' recorded'];
    if (s.sampled) parts.push(s.sampled+' sampled out');
    if (s.dropped) parts.push(s.dropped+' dropped');
    if (s.failed) parts.push(s.failed+' failed to store');
    el('stats').textContent = parts.join(' · ');

    el('pause').className = s.paused ? 'off' : '';
    el('pause').textContent = s.paused ? 'Paused' : 'Pause';

    var watchers = body.watchers || [];
    el('watching').textContent = watchers.length ? watchers.length+' watchers' : 'nothing watching';
    el('watching').title = watchers.join(', ');

    var notes = '';
    /* An empty table has several causes and only one is "no traffic". Saying
     * which is the difference between a useful page and a misleading one. */
    if (!watchers.length) {
      notes += '<div class="note warn">No watchers are installed, so nothing is being '+
        'recorded. An empty table here does not mean your application is idle.</div>';
    }
    if (s.paused) {
      notes += '<div class="note warn">Recording is paused in the process that served this '+
        'page. With more than one web process the others are still recording — this is a '+
        'development convenience, not a deployment switch.</div>';
    }
    if (s.dropped) {
      notes += '<div class="note warn">'+s.dropped+' entries were dropped because the queue '+
        'filled: the store is not keeping up. Recording never blocks a request, so this is '+
        'lost detail rather than lost throughput.</div>';
    }
    el('notices').innerHTML = notes;
  });
  fetch('api/counts').then(function(r){ return r.json(); }).then(function(body){
    renderNav(body.counts || {});
  });
}

function refresh(){ showChips(); load(false); stats(); retail(); }

function retail(){
  if (source) { source.close(); source = null; }
  if (!state.tailing) return;
  source = new EventSource('api/tail'+params());
  source.addEventListener('entry', function(ev){
    if (state.view) return;
    try {
      var e = JSON.parse(ev.data);
      e._fresh = true;
      state.rows.unshift(e);
      while (state.rows.length > 200) { state.rows.pop(); }
      renderList();
    } catch (err) { /* a frame we cannot read is not worth breaking the page over */ }
  });
  /* The server says so rather than going quiet, because a stream that ends
   * silently reads as a working stream with nothing to say. */
  source.addEventListener('gap', function(){
    state.tailing = false;
    paintTail();
    el('notices').innerHTML += '<div class="note warn">The live tail fell behind and was '+
      'closed, so some entries are not shown above. Reload to see them.</div>';
  });
}

function paintTail(){
  el('tail').className = state.tailing ? 'on' : '';
  el('tail').textContent = state.tailing ? 'Live' : 'Paused view';
}

/* ---- wiring ---------------------------------------------------------- */

el('reload').onclick = function(){ refresh(); };
el('level').onchange = function(){ state.level = this.value; refresh(); };
el('q').onkeydown = function(ev){ if (ev.key === 'Enter') { state.q = this.value; refresh(); } };
el('tags').onkeydown = function(ev){
  if (ev.key !== 'Enter') return;
  state.tags = this.value.split(',').map(function(t){ return t.trim(); })
    .filter(function(t){ return t !== ''; });
  refresh();
};
el('tail').onclick = function(){ state.tailing = !state.tailing; paintTail(); retail(); if (state.tailing) load(false); };
el('pause').onclick = function(){
  var paused = el('pause').className === 'off';
  fetch('api/pause'+(paused ? '?resume=1' : ''), {method:'POST'}).then(stats);
};
/* The button shows what you would get, not what you have: a sun on a dark
 * page is a promise, where a moon on a dark page is a label nobody reads the
 * same way twice. */
function paintTheme(){
  var explicit = document.documentElement.getAttribute('data-theme');
  var dark = explicit ? explicit === 'dark'
    : window.matchMedia('(prefers-color-scheme: dark)').matches;
  el('theme').innerHTML = dark ? '&#9728;' : '&#9790;';
  el('theme').title = dark ? 'Switch to light' : 'Switch to dark';
}

el('theme').onclick = function(){
  var explicit = document.documentElement.getAttribute('data-theme');
  var dark = explicit ? explicit === 'dark'
    : window.matchMedia('(prefers-color-scheme: dark)').matches;
  var next = dark ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  try { localStorage.setItem('observe.theme', next); }
  catch (err) { /* the choice holds for this page either way */ }
  paintTheme();
};

/* Following the operating system means following it as it changes, for
 * anybody who has not overridden it here. */
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function(){
  if (!document.documentElement.getAttribute('data-theme')) { paintTheme(); }
});

el('clear').onclick = function(){
  /* Confirmed, because it is the one control here that destroys something
   * and a misplaced click would take the evidence with it. */
  if (!window.confirm('Delete every recorded entry?')) return;
  fetch('api/clear', {method:'POST'}).then(function(){ refresh(); });
};

/* The filter bar sticks below the header, so it needs the header's measured
 * height rather than a guess: the controls wrap at narrow widths and a
 * hardcoded offset would leave a gap on one layout and an overlap on the
 * other. */
function measureHead(){
  var head = el('head').getBoundingClientRect().height;
  var bar = document.querySelector('.bar').getBoundingClientRect().height;
  var root = document.documentElement.style;
  root.setProperty('--headh', head+'px');
  /* The column headers pin below both, so they need the pair's height and
   * not either one. */
  root.setProperty('--stickyh', (head + bar)+'px');
}

el('title').textContent = panel().title;
el('subtitle').textContent = '';
paintTheme();
paintTail();
measureHead();
window.addEventListener('resize', measureHead);
refresh();
setInterval(stats, 15000);
`
