// Read-only captain board. Renders the embedded snapshot first (so an --out file opens with no network), then, when
// served over http, refreshes from the same-origin /data.json every 10s. It only reads and displays: there is no form,
// no button, no POST, no mutate. All values go in through textContent, never innerHTML, so board data cannot inject markup.
(function () {
  "use strict";

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = String(text);
    return e;
  }

  // num renders a scorecard metric: null/undefined -> "unknown" (never 0), else the value.
  function num(v) {
    if (v === null || v === undefined) return el("span", "unknown", "unknown");
    return document.createTextNode(String(v));
  }

  function stateClass(s) { return "pill state-" + String(s || "unknown"); }

  function renderStories(root, stories) {
    var tbl = el("table");
    var head = el("tr");
    ["story", "state", "attempt", "liveness", "composer", "forge", "steers", "questions", "wakes", "latest attempt"].forEach(function (h) {
      head.appendChild(el("th", null, h));
    });
    tbl.appendChild(head);
    (stories || []).forEach(function (s) {
      var tr = el("tr");
      tr.appendChild(el("td", null, s.id));
      var st = el("td"); st.appendChild(el("span", stateClass(s.state), s.state)); tr.appendChild(st);
      tr.appendChild(el("td", null, s.attempt));
      tr.appendChild(el("td", s.liveness === "unknown" ? "unknown" : null, s.liveness));
      tr.appendChild(el("td", s.composer === "unknown" ? "unknown" : null, s.composer));
      var fg = el("td"); fg.appendChild(el("code", null, s.forge)); tr.appendChild(fg);
      tr.appendChild(el("td", null, (s.steer_used || 0) + "/" + (s.steer_budget || 0)));
      tr.appendChild(el("td", s.questions_pending ? "warn" : null, s.questions_pending || 0));
      tr.appendChild(el("td", s.unacked_wakes ? "warn" : null, s.unacked_wakes || 0));
      var last = el("td", "attempts");
      var a = (s.attempts || [])[(s.attempts || []).length - 1];
      if (a) {
        last.appendChild(el("b", null, "a" + a.attempt + " "));
        last.appendChild(document.createTextNode("cost$ ")); last.appendChild(num(a.cost_usd));
        last.appendChild(document.createTextNode(" ci_s ")); last.appendChild(num(a.ci_wall_incl_queue_s));
      } else {
        last.appendChild(el("span", "empty", "no attempts"));
      }
      tr.appendChild(last);
      tbl.appendChild(tr);
    });
    root.appendChild(tbl);
  }

  function renderArena(root, a) {
    a = a || {};
    var ul = el("ul", "plain");
    ul.appendChild(el("li", null, "round: " + (a.round || 0)));
    var r2 = el("li", a.round2 ? "warn" : "ok", "round2: " + (a.round2 ? "yes" : "no"));
    ul.appendChild(r2);
    (a.round2_reasons || []).forEach(function (r) { ul.appendChild(el("li", "muted", "- " + r)); });
    ul.appendChild(el("li", a.design_signed ? "ok" : "warn", "design signed: " + (a.design_signed ? "yes" : "no")));
    ul.appendChild(el("li", a.synthesis_ready ? null : "muted", "synthesis: " + (a.synthesis_ready ? "ready" : "none")));
    root.appendChild(ul);
  }

  function renderWake(root, w) {
    if (!w) { root.appendChild(el("div", "empty", "no unacked wake")); return; }
    var ul = el("ul", "plain");
    ul.appendChild(el("li", null, "gen " + w.gen + " " + w.kind + " (" + (w.story || "") + ")"));
    if (w.note) ul.appendChild(el("li", "muted", w.note));
    if (w.ts) ul.appendChild(el("li", "muted", w.ts));
    root.appendChild(ul);
  }

  function renderQuota(root, rows) {
    if (!rows || !rows.length) { root.appendChild(el("div", "empty", "no quota readings")); return; }
    var tbl = el("table");
    var head = el("tr");
    ["harness", "model", "percent", "runway", "source"].forEach(function (h) { head.appendChild(el("th", null, h)); });
    tbl.appendChild(head);
    rows.forEach(function (r) {
      var tr = el("tr");
      tr.appendChild(el("td", null, r.harness));
      tr.appendChild(el("td", null, r.model || "-"));
      if (r.known) {
        tr.appendChild(el("td", r.percent < 10 ? "warn" : null, r.percent + "%"));
        tr.appendChild(el("td", r.runway === "exhausted_now" ? "warn" : null, r.runway));
      } else {
        var pu = el("td", "unknown"); pu.appendChild(el("span", "unknown", "unknown"));
        if (r.reason) pu.title = r.reason;
        tr.appendChild(pu);
        tr.appendChild(el("td", "unknown", "unknown"));
      }
      tr.appendChild(el("td", "muted", r.source));
      tbl.appendChild(tr);
    });
    root.appendChild(tbl);
  }

  function renderPending(root, items) {
    if (!items || !items.length) { root.appendChild(el("div", "empty", "nothing waiting on the captain")); return; }
    var ul = el("ul", "plain");
    items.forEach(function (it) { ul.appendChild(el("li", "warn", it)); });
    root.appendChild(ul);
  }

  function render(data) {
    data = data || {};
    document.getElementById("epic").textContent = data.epic || "?";
    document.getElementById("generated").textContent = data.generated_at || "";
    var slots = ["stories", "arena", "wake", "quota", "pending"];
    slots.forEach(function (id) { var n = document.getElementById(id); if (n) n.textContent = ""; });
    renderStories(document.getElementById("stories"), data.stories);
    renderArena(document.getElementById("arena"), data.arena);
    renderWake(document.getElementById("wake"), data.latest_wake);
    renderQuota(document.getElementById("quota"), data.quota);
    renderPending(document.getElementById("pending"), data.pending_captain);
  }

  render(window.__BOARD_DATA__ || {});

  // Live refresh only when served (not opened from a local file); an --out file keeps its embedded snapshot offline.
  if (location.protocol !== "file:") {
    setInterval(function () {
      fetch("data.json", { cache: "no-store" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) { if (d) render(d); })
        .catch(function () { /* keep the last good snapshot */ });
    }, 10000);
  }
})();
