package render

import "html/template"

// pageTemplate is the whole reading page. CSS and JS are inline and static;
// only escaped text is interpolated. The CSP meta tag forbids any network
// load even if a browser mis-parsed something.
var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

const pageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<meta name="referrer" content="no-referrer">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="generator" content="Athar">
<title>{{.DocTitle}}</title>
<style>
:root {
  color-scheme: light dark;
  --bg: #f6f6f4; --fg: #1d1d1f; --muted: #6a6a6a; --card: #ffffff; --border: #e0e0dc;
  --user: #eef4ff; --user-border: #c8d8f8; --code: #f1f1ee; --hit: #d98e04; --focus: #2f6fd6;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161719; --fg: #e4e4e4; --muted: #9b9b9b; --card: #1f2023; --border: #34363b;
    --user: #1b263a; --user-border: #2d4266; --code: #18191b; --hit: #f2b233; --focus: #7aa7ff;
  }
}
* { box-sizing: border-box; }
html { background: var(--bg); }
body {
  margin: 0 auto; max-width: 920px; padding: 24px 16px 96px;
  background: var(--bg); color: var(--fg);
  font: 16px/1.65 "Segoe UI", Tahoma, "Noto Sans Arabic", "Noto Naskh Arabic", "Geeza Pro", system-ui, sans-serif;
}
header.page { border-bottom: 1px solid var(--border); padding-bottom: 12px; margin-bottom: 8px; }
header.page h1 { font-size: 1.45rem; line-height: 1.35; margin: 0 0 6px; unicode-bidi: plaintext; text-align: start; overflow-wrap: anywhere; }
.meta { color: var(--muted); font-size: .88rem; margin: 0; overflow-wrap: anywhere; }
.meta .agent { color: var(--fg); font-weight: 600; }
.note { margin: 8px 0 0; font-size: .88rem; color: var(--muted); font-style: italic; }
.empty { color: var(--muted); font-style: italic; margin: 24px 0; }
.msg {
  background: var(--card); border: 1px solid var(--border);
  border-radius: 8px; padding: 8px 14px 10px 18px; margin: 10px 0; scroll-margin: 16px;
  outline: 3px solid transparent; outline-offset: 2px;
}
.msg.role-user { background: var(--user); border-color: var(--user-border); }
.msg.kind-tool_call, .msg.kind-tool_output { padding: 4px 14px 6px 18px; font-size: .9rem; }
.msg.hit { box-shadow: inset 5px 0 0 var(--hit); }
.msg.flash { animation: flash 1.4s ease-out; }
@keyframes flash { from { outline-color: var(--hit); } to { outline-color: transparent; } }
.msg > header { display: flex; gap: 10px; align-items: baseline; font-size: .8rem; color: var(--muted); margin-bottom: 2px; }
.msg > header .role { font-weight: 600; color: var(--fg); }
.text { white-space: pre-wrap; overflow-wrap: anywhere; unicode-bidi: plaintext; text-align: start; }
.mono { font-family: "Cascadia Mono", Consolas, ui-monospace, monospace; font-size: .85rem; line-height: 1.5; }
details > summary {
  cursor: pointer; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  font-family: "Cascadia Mono", Consolas, ui-monospace, monospace; font-size: .85rem;
}
details > .text { margin: 6px 0 2px; padding: 8px 10px; background: var(--code); border-radius: 6px; max-height: 70vh; overflow: auto; }
.more { margin: 4px 0 0; color: var(--muted); font-style: italic; font-size: .85rem; }
#hitnav {
  position: fixed; right: 16px; bottom: 16px; display: flex; gap: 2px; align-items: center;
  background: var(--card); color: var(--fg); border: 1px solid var(--border); border-radius: 999px;
  padding: 3px 4px 3px 14px; font-size: .88rem; box-shadow: 0 2px 10px rgba(0, 0, 0, .18);
}
#hitnav span { margin-right: 6px; }
#hitnav button {
  border: 0; background: transparent; color: var(--fg); cursor: pointer; font: inherit;
  padding: 4px 9px; border-radius: 999px;
}
#hitnav button:hover { background: var(--code); }
#hitnav button:focus-visible { outline: 2px solid var(--focus); }
footer.page { margin-top: 28px; color: var(--muted); font-size: .8rem; }
@media print {
  #hitnav { display: none; }
  details > .text { max-height: none; }
}
</style>
</head>
<body>
<header class="page">
<h1 dir="auto">{{.Title}}</h1>
<p class="meta" dir="ltr"><span class="agent">{{.Agent}}</span> · <bdi class="path">{{.Project}}</bdi>{{if .Started}} · started <time datetime="{{.StartedISO}}">{{.Started}}</time>{{end}}{{if .Updated}} · last active <time datetime="{{.UpdatedISO}}">{{.Updated}}</time>{{end}} · {{.Count}} · coverage: {{.Coverage}}</p>
{{- if .CoverageNote}}
<p class="note" dir="ltr">{{.CoverageNote}}</p>
{{- end}}
</header>
<main>
{{- range .Messages}}
<article dir="auto" class="{{.Class}}"{{if .Anchor}} id="hit"{{end}} data-i="{{.Index}}">
<header dir="ltr"><span class="role">{{.Role}}</span>{{if .Time}}<time datetime="{{.TimeISO}}" title="{{.TimeFull}}">{{.Time}}</time>{{end}}</header>
{{- if .Tool}}
<details{{if .Open}} open{{end}}><summary dir="ltr">{{.Summary}}</summary><div class="text mono">{{.Text}}</div>{{if .More}}<p class="more" dir="ltr">{{.More}}</p>{{end}}</details>
{{- else}}
<div class="text">{{.Text}}</div>
{{- end}}
</article>
{{- else}}
<p class="empty">No messages were archived for this session.</p>
{{- end}}
</main>
<footer class="page" dir="ltr">Generated by Athar on {{.Generated}}. This page is a local file and loads nothing from the network.</footer>
{{- if .HitCount}}
<nav id="hitnav" dir="ltr" aria-label="Matches"><span>hit <b id="hitpos">{{.HitPos}}</b>/<span id="hitcount">{{.HitCount}}</span></span><button type="button" id="hitprev" title="Previous match (P)" aria-label="Previous match">▲</button><button type="button" id="hitnext" title="Next match (N)" aria-label="Next match">▼</button></nav>
{{- end}}
<script>
(function () {
  function reveal(el) {
    if (!el) return;
    var d = el.querySelector('details');
    if (d) d.open = true;
  }
  // Center a message on screen; a message taller than the window is shown
  // from its beginning instead.
  function show(el, smooth) {
    var tall = el.getBoundingClientRect().height > window.innerHeight * 0.8;
    el.scrollIntoView({block: tall ? 'start' : 'center', behavior: smooth ? 'smooth' : 'auto'});
  }
  var start = document.getElementById('hit');
  reveal(start);
  if (start) show(start, false);

  var nav = document.getElementById('hitnav');
  if (!nav) return;
  var hits = Array.prototype.slice.call(document.querySelectorAll('article.hit'));
  var pos = document.getElementById('hitpos');
  var cur = hits.indexOf(start);
  function go(step) {
    if (!hits.length) return;
    if (cur < 0) cur = step > 0 ? 0 : hits.length - 1;
    else cur = (cur + step + hits.length) % hits.length;
    var el = hits[cur];
    reveal(el);
    show(el, true);
    el.classList.remove('flash');
    void el.offsetWidth;
    el.classList.add('flash');
    pos.textContent = String(cur + 1);
  }
  document.getElementById('hitprev').addEventListener('click', function () { go(-1); });
  document.getElementById('hitnext').addEventListener('click', function () { go(1); });
  document.addEventListener('keydown', function (e) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    var t = e.target && e.target.tagName;
    if (t === 'INPUT' || t === 'TEXTAREA') return;
    if (e.code === 'KeyN') { go(1); e.preventDefault(); }
    else if (e.code === 'KeyP') { go(-1); e.preventDefault(); }
  });
})();
</script>
</body>
</html>
`
