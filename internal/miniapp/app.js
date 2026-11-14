// rex-tg Mini App: shows one page: an answer, the changes of a turn, the
// live screen of a topic's pane, the files of the agent's folder, the
// changes of its repository, or the control panel (the agents in Rex panes:
// open, rename, start, unlink, pause).
// Every request carries Telegram's signed init data; the server checks it.
(function () {
  var tg = window.Telegram && window.Telegram.WebApp;
  if (tg) { tg.ready(); tg.expand(); }
  var page = document.getElementById("page");
  var title = document.getElementById("title");

  // --- the top bar: text size and theme, kept on this phone ---

  var root = document.documentElement;
  function load(k) {
    try { return localStorage.getItem(k); } catch (e) { return null; }
  }
  function save(k, v) {
    try {
      if (v === null) { localStorage.removeItem(k); } else { localStorage.setItem(k, v); }
    } catch (e) { /* no storage: the choice lasts while the page is open */ }
  }

  var scale = 1, scaleHooks = [];
  var resetButton = document.getElementById("reset");
  function setScale(s) {
    scale = Math.round(Math.min(1.6, Math.max(0.6, s)) * 10) / 10;
    root.style.setProperty("--scale", String(scale));
    save("rex-tg.scale", scale === 1 ? null : String(scale));
    resetButton.textContent = Math.round(scale * 100) + "%";
    scaleHooks.forEach(function (f) { f(); });
  }
  document.getElementById("smaller").addEventListener("click", function () { setScale(scale - 0.1); });
  document.getElementById("bigger").addEventListener("click", function () { setScale(scale + 0.1); });
  document.getElementById("reset").addEventListener("click", function () { setScale(1); });
  setScale(parseFloat(load("rex-tg.scale")) || 1);

  // Telegram's own scheme, which the page follows until ☀/🌙 is tapped.
  function telegramDark() {
    if (tg && tg.colorScheme) { return tg.colorScheme === "dark"; }
    return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  }
  function isDark() {
    var t = root.getAttribute("data-theme");
    return t ? t === "dark" : telegramDark();
  }
  var themeButton = document.getElementById("theme");
  // The button shows what a tap gives: the sun in the dark, the moon in the light.
  var sunIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4.2"/><path d="M12 2.5v2.2M12 19.3v2.2M4.6 4.6l1.6 1.6M17.8 17.8l1.6 1.6M2.5 12h2.2M19.3 12h2.2M4.6 19.4l1.6-1.6M17.8 6.2l1.6-1.6"/></svg>';
  var moonIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M20.5 14.6A8.5 8.5 0 0 1 9.4 3.5 8.5 8.5 0 1 0 20.5 14.6z"/></svg>';
  function setTheme(t) {
    if (t === "dark" || t === "light") { root.setAttribute("data-theme", t); } else { t = null; root.removeAttribute("data-theme"); }
    save("rex-tg.theme", t);
    themeButton.innerHTML = isDark() ? sunIcon : moonIcon; // constant icons
    themeButton.setAttribute("aria-label", isDark() ? "Light theme" : "Dark theme");
    if (tg && tg.setHeaderColor) {
      try {
        var bg = getComputedStyle(root).getPropertyValue("--bg").trim();
        tg.setHeaderColor(t ? bg : "bg_color");
        if (tg.setBackgroundColor) { tg.setBackgroundColor(t ? bg : "bg_color"); }
      } catch (e) { /* an older Telegram: only the page changes */ }
    }
  }
  themeButton.addEventListener("click", function () {
    var want = isDark() ? "light" : "dark";
    // Tapping back to Telegram's own scheme follows Telegram again.
    setTheme(want === (telegramDark() ? "dark" : "light") ? null : want);
  });
  if (tg && tg.onEvent) { tg.onEvent("themeChanged", function () { setTheme(root.getAttribute("data-theme")); }); }
  setTheme(load("rex-tg.theme"));
  var id = new URLSearchParams(location.search).get("p") || "";
  var initData = tg ? tg.initData : "";
  if (!initData) {
    page.textContent = "Open this page from its button in Telegram.";
    return;
  }

  // api reads path; with a body it posts it as JSON.
  function api(path, body) {
    var opt = { headers: { "X-Telegram-Init-Data": initData } };
    if (body !== undefined) {
      opt.method = "POST";
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    return fetch(path, opt).then(function (r) {
      return r.json().catch(function () { return null; }).then(function (body) {
        if (!r.ok || !body) {
          var err = new Error((body && body.error) || ("HTTP " + r.status));
          err.status = r.status;
          throw err;
        }
        return body;
      });
    });
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    if (text !== undefined) { e.textContent = text; }
    return e;
  }

  function note(msg) {
    page.className = "note";
    page.textContent = msg;
  }

  function setTitle(t) {
    title.textContent = t || "rex-tg";
    document.title = t || "rex-tg";
  }

  // --- the live screen ---

  // loadTerminal loads xterm.js (served by rex-tg) once.
  var terminalLoad = null;
  function loadTerminal() {
    if (window.Terminal) { return Promise.resolve(); }
    if (!terminalLoad) {
      terminalLoad = new Promise(function (resolve, reject) {
        var css = document.createElement("link");
        css.rel = "stylesheet";
        css.href = "xterm-5.5.0.css";
        document.head.appendChild(css);
        var js = document.createElement("script");
        js.src = "xterm-5.5.0.js";
        js.onload = function () { if (window.Terminal) { resolve(); } else { reject(new Error("no terminal")); } };
        js.onerror = function () { reject(new Error("no terminal")); };
        document.head.appendChild(js);
      });
    }
    return terminalLoad;
  }

  // width is the number of terminal columns of a line, about as a terminal
  // counts them: wide characters (CJK, most emoji) take two.
  function width(line) {
    var w = 0;
    for (var ch of line) {
      var c = ch.codePointAt(0);
      if (c === 0x200d || (c >= 0xfe00 && c <= 0xfe0f) || (c >= 0x300 && c <= 0x36f)) { continue; }
      w += (c >= 0x1100 && (c <= 0x115f || (c >= 0x2e80 && c <= 0xa4cf) || (c >= 0xac00 && c <= 0xd7a3) ||
        (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe4f) || (c >= 0xff00 && c <= 0xff60) ||
        (c >= 0xffe0 && c <= 0xffe6) || (c >= 0x1f300 && c <= 0x1faff) || (c >= 0x20000 && c <= 0x3fffd))) ? 2 : 1;
    }
    return w;
  }

  function liveScreen() {
    document.body.classList.add("fill");
    page.className = "";
    page.textContent = "";
    var bar = el("div", "bar");
    var status = el("span", "status", "Loading…");
    var chips = el("div", "chips");
    var pause = el("button", "chip", "Pause");
    var fit = el("button", "chip", "Fit width");
    chips.appendChild(pause);
    chips.appendChild(fit);
    bar.appendChild(status);
    bar.appendChild(chips);
    var wrap = el("div", "term-wrap");
    var jump = el("button", "jump", "↓");
    jump.hidden = true;
    jump.setAttribute("aria-label", "To the bottom");
    page.appendChild(bar);
    page.appendChild(wrap);
    page.appendChild(jump);

    var fitOn = load("rex-tg.fit") === "1", paused = false;
    var term = null, pre = null, last = null, follow = true;
    var mono = "ui-monospace, SFMono-Regular, Menlo, monospace";

    // ratio is the width of a character of the font for a size of 1px.
    var ratio = 0.6;
    try {
      var cx = document.createElement("canvas").getContext("2d");
      cx.font = "100px " + mono;
      ratio = cx.measureText("W").width / 100 || 0.6;
    } catch (e) { /* the usual ratio */ }

    function fontSize(cols) {
      var base = 11 * scale;
      if (!fitOn) { return base; }
      var px = (wrap.clientWidth - 18) / (cols * ratio);
      return Math.max(4, Math.min(base * 1.6, Math.floor(px * 10) / 10));
    }

    function atBottom() { return wrap.scrollTop + wrap.clientHeight >= wrap.scrollHeight - 24; }
    function toBottom() { wrap.scrollTop = wrap.scrollHeight; }
    wrap.addEventListener("scroll", function () {
      follow = atBottom();
      jump.hidden = follow;
    });
    jump.addEventListener("click", function () { follow = true; toBottom(); jump.hidden = true; });

    function draw() {
      if (!last) { return; }
      var plain = last.text || "";
      var lines = plain.split("\n");
      var cols = 20;
      lines.forEach(function (l) { cols = Math.max(cols, width(l) + 1); });
      if (term && last.vt) {
        var fs = fontSize(cols);
        if (term.options.fontSize !== fs) { term.options.fontSize = fs; }
        term.resize(cols, lines.length);
        // Hide the cursor, clear, and write the lines in place.
        term.write("\x1b[?25l\x1b[H\x1b[2J" + last.vt.split("\n").join("\r\n"), function () {
          if (follow) { toBottom(); }
        });
        return;
      }
      if (!pre) {
        pre = el("pre", "screen");
        wrap.textContent = "";
        wrap.appendChild(pre);
      }
      pre.style.fontSize = fontSize(cols) + "px";
      if (pre.textContent !== plain) { pre.textContent = plain; }
      if (follow) { toBottom(); }
    }

    function setFit(on) {
      fitOn = on;
      fit.classList.toggle("on", fitOn);
      save("rex-tg.fit", fitOn ? "1" : null);
      draw();
    }
    fit.addEventListener("click", function () { setFit(!fitOn); });
    setFit(fitOn);
    scaleHooks.push(draw);
    window.addEventListener("resize", function () { if (fitOn) { draw(); } });

    var timer = null;
    function schedule() {
      clearTimeout(timer);
      timer = setTimeout(refresh, 2000);
    }
    pause.addEventListener("click", function () {
      paused = !paused;
      pause.classList.toggle("on", paused);
      pause.textContent = paused ? "Resume" : "Pause";
      if (paused) {
        clearTimeout(timer);
        status.textContent = "⏸ Paused" + (last && last.at ? " · " + last.at : "");
        status.className = "status";
      } else {
        refresh();
      }
    });
    function refresh() {
      if (paused) { return; }
      if (document.hidden) { schedule(); return; }
      api("api/screen?id=" + encodeURIComponent(id))
        .then(function (s) {
          if (paused) { return; }
          if (s.error) {
            status.textContent = s.error;
            status.className = "status off";
            return;
          }
          if (!last || last.text !== s.text || last.vt !== s.vt) {
            last = s;
            draw();
          }
          status.textContent = "● Live · " + s.at;
          status.className = "status live";
        })
        .catch(function (e) {
          status.textContent = e.message || "Could not read the screen.";
          status.className = "status off";
        })
        .then(schedule);
    }
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !paused) { refresh(); }
    });

    loadTerminal()
      .then(function () {
        wrap.textContent = "";
        pre = null;
        term = new window.Terminal({
          disableStdin: true, cursorBlink: false, cursorStyle: "bar", cursorInactiveStyle: "none",
          scrollback: 0, fontFamily: mono, fontSize: 11 * scale, lineHeight: 1.1, cols: 80, rows: 24,
          theme: { background: "#1e1e1e", foreground: "#d4d4d4", cursor: "#1e1e1e", cursorAccent: "#1e1e1e" }
        });
        term.open(wrap);
        draw();
      })
      .catch(function () { /* no terminal: the plain text stays */ })
      .then(refresh);
  }

  // --- trees: the file explorer and the changes ---

  // Telegram's back button runs the one action set last (none: hidden).
  var backAction = null;
  if (tg && tg.BackButton) { tg.BackButton.onClick(function () { if (backAction) { backAction(); } }); }
  function setBack(f) {
    backAction = f;
    if (tg && tg.BackButton) { if (f) { tg.BackButton.show(); } else { tg.BackButton.hide(); } }
  }

  function size(n) {
    if (n < 1000) { return n + " B"; }
    if (n < 1000 * 1000) { return (n / 1000).toFixed(1) + " KB"; }
    return (n / 1000 / 1000).toFixed(1) + " MB";
  }

  // treeRow is one row of a tree at a depth: a folder (with a chevron) or a
  // file. lead is an element before the name (an icon or a badge).
  function treeRow(depth, dir, name, lead) {
    var row = el("div", "trow" + (dir ? " dir" : ""));
    row.style.setProperty("--d", String(depth));
    row.setAttribute("role", "treeitem");
    row.setAttribute("aria-level", String(depth + 1));
    row.tabIndex = 0;
    row.appendChild(el("span", "chev", dir ? "›" : ""));
    if (lead) { row.appendChild(lead); }
    row.appendChild(el("span", "nm", name));
    row.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); row.click(); }
    });
    return row;
  }

  // views makes the two views of a tree page: the list, and one item.
  function views() {
    page.className = "";
    page.textContent = "";
    var list = el("div", "view"), item = el("div", "view");
    item.hidden = true;
    page.appendChild(list);
    page.appendChild(item);
    var scrollY = 0;
    return {
      list: list, item: item,
      showItem: function (back) {
        if (!list.hidden) { scrollY = window.scrollY; }
        list.hidden = true;
        item.hidden = false;
        window.scrollTo(0, 0);
        setBack(back);
      },
      showList: function () {
        item.hidden = true;
        item.textContent = "";
        list.hidden = false;
        window.scrollTo(0, scrollY);
        setBack(null);
      }
    };
  }

  function chip(text, label) {
    var b = el("button", "chip", text);
    b.type = "button";
    if (label) { b.setAttribute("aria-label", label); }
    return b;
  }

  // --- the file explorer ---

  function fileBrowser(rootTitle) {
    var v = views();
    var open = {}; // the folders that are open, by path
    var bar = el("div", "bar");
    var name = el("span", "status", rootTitle.replace(/^📂\s*/, ""));
    var chips = el("div", "chips");
    var collapse = chip("Collapse all"), reload = chip("↻", "Load again");
    chips.appendChild(collapse);
    chips.appendChild(reload);
    bar.appendChild(name);
    bar.appendChild(chips);
    var tree = el("div", "tree");
    tree.setAttribute("role", "tree");
    v.list.appendChild(bar);
    v.list.appendChild(tree);

    function load(path, box, depth) {
      box.textContent = "";
      var wait = el("div", "tnote", "Loading…");
      wait.style.setProperty("--d", String(depth));
      box.appendChild(wait);
      return api("api/files?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(path))
        .then(function (d) {
          box.textContent = "";
          if (d.note) { var n = el("div", "tnote", d.note); n.style.setProperty("--d", String(depth)); box.appendChild(n); }
          (d.entries || []).forEach(function (e) {
            var p = path ? path + "/" + e.name : e.name;
            var row = treeRow(depth, e.dir, e.name, el("span", "ic", e.dir ? "📁" : "📄"));
            if (!e.dir) { row.appendChild(el("span", "tmeta", size(e.size))); }
            box.appendChild(row);
            if (!e.dir) {
              row.addEventListener("click", function () { showFile(p); });
              return;
            }
            var kids = el("div", "kids");
            kids.setAttribute("role", "group");
            kids.hidden = true;
            box.appendChild(kids);
            var loaded = false;
            function toggle(on) {
              row.classList.toggle("open", on);
              row.setAttribute("aria-expanded", on ? "true" : "false");
              row.firstChild.nextSibling.textContent = on ? "📂" : "📁";
              kids.hidden = !on;
              if (on) { open[p] = true; } else { delete open[p]; }
              if (on && !loaded) { loaded = true; load(p, kids, depth + 1); }
            }
            row.setAttribute("aria-expanded", "false");
            row.addEventListener("click", function () { toggle(!row.classList.contains("open")); });
            if (open[p]) { toggle(true); }
          });
          if (!d.entries || d.entries.length === 0) {
            var empty = el("div", "tnote", "Empty folder");
            empty.style.setProperty("--d", String(depth));
            box.appendChild(empty);
          }
        })
        .catch(function (e) {
          box.textContent = "";
          var n = el("div", "tnote", e.message || "This folder could not be loaded.");
          n.style.setProperty("--d", String(depth));
          box.appendChild(n);
        });
    }

    function showFile(p) {
      v.showItem(v.showList);
      var head = el("div", "itembar");
      var back = chip("‹ Files");
      back.addEventListener("click", v.showList);
      head.appendChild(back);
      head.appendChild(el("code", "path", p));
      v.item.appendChild(head);
      var body = el("div", "content", "");
      body.appendChild(el("p", "note", "Loading…"));
      v.item.appendChild(body);
      api("api/files?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(p))
        .then(function (d) {
          body.textContent = "";
          if (d.note) { body.appendChild(el("p", "note", d.note)); }
          if (d.html) {
            var c = el("div", "");
            c.innerHTML = d.html; // rendered and escaped by rex-tg
            body.appendChild(c);
          }
        })
        .catch(function (e) {
          body.textContent = "";
          body.appendChild(el("p", "note", e.message || "This file could not be loaded."));
        });
    }

    collapse.addEventListener("click", function () { open = {}; load("", tree, 0); });
    reload.addEventListener("click", function () { load("", tree, 0); });
    load("", tree, 0);
  }

  // --- the changes of a repository ---

  var statusLetter = { modified: "M", added: "A", deleted: "D", untracked: "U", type: "T", conflict: "!" };
  var statusName = {
    modified: "Modified", added: "Added", deleted: "Deleted", untracked: "New, not tracked",
    type: "Type changed", conflict: "Conflict"
  };

  function badge(status) {
    var b = el("span", "badge " + status, statusLetter[status] || "?");
    b.title = statusName[status] || status;
    b.setAttribute("aria-label", statusName[status] || status);
    return b;
  }

  function counts(added, removed, binary) {
    var m = el("span", "tmeta");
    if (binary) { m.textContent = "binary"; return m; }
    if (added) { m.appendChild(el("span", "plus", "+" + added)); }
    if (added && removed) { m.appendChild(document.createTextNode(" ")); }
    if (removed) { m.appendChild(el("span", "minus", "−" + removed)); }
    return m;
  }

  // buildTree turns the changed paths into folders: a folder that holds
  // only one folder shows as one row ("internal/daemon").
  function buildTree(files) {
    var root = { name: "", path: "", dirs: {}, files: [] };
    files.forEach(function (f) {
      var parts = f.path.split("/"), n = root;
      for (var i = 0; i < parts.length - 1; i++) {
        var p = n.path ? n.path + "/" + parts[i] : parts[i];
        n = n.dirs[parts[i]] || (n.dirs[parts[i]] = { name: parts[i], path: p, dirs: {}, files: [] });
      }
      n.files.push({ name: parts[parts.length - 1], f: f });
    });
    function compact(n) {
      Object.keys(n.dirs).forEach(function (k) {
        var d = n.dirs[k];
        while (d.files.length === 0 && Object.keys(d.dirs).length === 1) {
          var only = d.dirs[Object.keys(d.dirs)[0]];
          d = { name: d.name + "/" + only.name, path: only.path, dirs: only.dirs, files: only.files };
        }
        n.dirs[k] = d;
        compact(d);
      });
    }
    compact(root);
    return root;
  }

  function changesView() {
    var v = views();
    var bases = [
      { key: "head", label: "Uncommitted" },
      { key: "branch", label: "This branch" },
      { key: "turn", label: "Last turn" }
    ];
    // base "" lets rex-tg choose: the first of the three that shows changes.
    var base = "", order = [], shut = {}, asked = 0, shown = 0;
    var wrapOn = load("rex-tg.wrap") === "1";

    var tabs = el("div", "tabs");
    tabs.setAttribute("role", "tablist");
    var tabButtons = {};
    bases.forEach(function (b) {
      var t = el("button", "");
      t.type = "button";
      t.setAttribute("role", "tab");
      t.appendChild(el("span", "", b.label));
      t.appendChild(el("small", "", ""));
      t.addEventListener("click", function () {
        if (t.getAttribute("aria-disabled") === "true") {
          var keep = sum.textContent;
          sum.textContent = t.title;
          setTimeout(function () { if (sum.textContent === t.title) { sum.textContent = keep; } }, 4000);
          return;
        }
        base = b.key;
        refresh();
      });
      tabButtons[b.key] = t;
      tabs.appendChild(t);
    });
    var bar = el("div", "bar");
    var sum = el("span", "status", "Loading…");
    var chips = el("div", "chips");
    var fold = chip("Collapse all"), reload = chip("↻", "Load again");
    chips.appendChild(fold);
    chips.appendChild(reload);
    bar.appendChild(sum);
    bar.appendChild(chips);
    var tree = el("div", "tree");
    tree.setAttribute("role", "tree");
    v.list.appendChild(tabs);
    v.list.appendChild(bar);
    v.list.appendChild(tree);

    function setTabs(l) {
      var hints = {
        head: ["", ""],
        branch: [l.main ? "vs " + l.main : "", "There is no main branch to compare with."],
        turn: [l.turn_at ? "at " + l.turn_at : "", "No turn of the agent was kept yet. rex-tg keeps one when [display] show_changes is on."]
      };
      bases.forEach(function (b) {
        var t = tabButtons[b.key], on = b.key === l.base;
        var avail = b.key === "head" || (b.key === "branch" ? !!l.main : !!l.turn_at);
        t.classList.toggle("on", on);
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.setAttribute("aria-disabled", avail ? "false" : "true");
        t.title = avail ? "" : hints[b.key][1];
        t.lastChild.textContent = hints[b.key][0] || (b.key === "head" ? "vs last commit" : "—");
      });
    }

    function render(l) {
      setTabs(l);
      var text = (l.branch ? "🌿 " + l.branch + " · " : "");
      if (!l.total) {
        text += "No changes";
      } else {
        text += l.total + (l.total === 1 ? " file" : " files") + " · +" + l.added + " −" + l.removed;
      }
      sum.textContent = text + (l.at ? " · " + l.at : "");
      tree.textContent = "";
      order = [];
      order.base = l.base;
      if (l.note) { tree.appendChild(el("div", "tnote", l.note)); }
      if (!l.files || !l.files.length) {
        var none = { head: "Nothing is changed since the last commit.", branch: "This branch has no changes of its own.", turn: "The last turn changed no files." };
        tree.appendChild(el("p", "note empty", none[l.base] || "No changes."));
        return;
      }
      (function walk(n, box, depth) {
        Object.keys(n.dirs).sort().forEach(function (k) {
          var d = n.dirs[k];
          var row = treeRow(depth, true, d.name, el("span", "ic", "📁"));
          var kids = el("div", "kids");
          kids.setAttribute("role", "group");
          function set(on) {
            row.classList.toggle("open", on);
            row.setAttribute("aria-expanded", on ? "true" : "false");
            row.firstChild.nextSibling.textContent = on ? "📂" : "📁";
            kids.hidden = !on;
            if (on) { delete shut[d.path]; } else { shut[d.path] = true; }
          }
          row.addEventListener("click", function () { set(!row.classList.contains("open")); syncFold(); });
          box.appendChild(row);
          box.appendChild(kids);
          walk(d, kids, depth + 1);
          set(!shut[d.path]);
        });
        n.files.sort(function (a, b) { return a.name < b.name ? -1 : a.name > b.name ? 1 : 0; }).forEach(function (x) {
          var f = x.f, at = order.length;
          order.push(f);
          var row = treeRow(depth, false, x.name, badge(f.status));
          if (f.hidden) { row.classList.add("hidden"); row.appendChild(el("span", "tmeta", "🔒")); } else { row.appendChild(counts(f.added, f.removed, f.binary)); }
          if (f.status === "deleted") { row.classList.add("gone"); }
          row.addEventListener("click", function () { showChange(at); });
          box.appendChild(row);
        });
      })(buildTree(l.files), tree, 0);
      syncFold();
    }

    function syncFold() {
      fold.textContent = tree.querySelector(".trow.dir.open") ? "Collapse all" : "Expand all";
    }

    function refresh() {
      sum.textContent = "Loading…";
      var mine = ++asked; // only the newest answer is drawn: tabs can be tapped fast
      return api("api/changes?id=" + encodeURIComponent(id) + "&base=" + encodeURIComponent(base))
        .then(function (l) {
          if (mine !== asked) { return; }
          base = l.base; // the view stays on it, also when it loads again
          render(l);
        })
        .catch(function (e) {
          if (mine !== asked) { return; }
          if (base && e.status === 404) { // a base that is gone, such as a branch merged: let rex-tg choose
            base = "";
            return refresh();
          }
          sum.textContent = e.message || "Could not load the changes.";
          tree.textContent = "";
        });
    }

    // showChange opens file at of list (the list shown when the first file
    // was opened: a refresh meanwhile does not move Previous and Next).
    function showChange(at, list) {
      list = list || order;
      var f = list[at], mine = ++shown;
      if (!f) { return; }
      v.showItem(v.showList);
      v.item.textContent = "";
      var head = el("div", "itembar");
      var back = chip("‹ Changes");
      back.addEventListener("click", v.showList);
      var nav = el("div", "chips");
      var prev = chip("‹", "Previous file"), next = chip("›", "Next file");
      var wrap = chip("Wrap", "Wrap long lines");
      wrap.classList.toggle("on", wrapOn);
      wrap.setAttribute("aria-pressed", wrapOn ? "true" : "false");
      prev.disabled = at === 0;
      next.disabled = at === list.length - 1;
      prev.addEventListener("click", function () { showChange(at - 1, list); });
      next.addEventListener("click", function () { showChange(at + 1, list); });
      nav.appendChild(wrap);
      nav.appendChild(prev);
      nav.appendChild(el("span", "count", (at + 1) + "/" + list.length));
      nav.appendChild(next);
      head.appendChild(back);
      head.appendChild(nav);
      v.item.appendChild(head);
      var title = el("div", "ftitle");
      title.appendChild(badge(f.status));
      title.appendChild(el("code", "path", f.path));
      if (!f.hidden) { title.appendChild(counts(f.added, f.removed, f.binary)); }
      v.item.appendChild(title);
      var body = el("div", "content");
      body.appendChild(el("p", "note", "Loading…"));
      v.item.appendChild(body);
      wrap.addEventListener("click", function () {
        wrapOn = !wrapOn;
        save("rex-tg.wrap", wrapOn ? "1" : null);
        wrap.classList.toggle("on", wrapOn);
        wrap.setAttribute("aria-pressed", wrapOn ? "true" : "false");
        var d = body.querySelector(".fdiff");
        if (d) { d.classList.toggle("wrap", wrapOn); }
      });
      var url = "api/changes?id=" + encodeURIComponent(id) + "&base=" + encodeURIComponent(list.base) +
        "&path=" + encodeURIComponent(f.path) + (f.status === "untracked" ? "&new=1" : "");
      api(url)
        .then(function (d) {
          if (mine !== shown || v.item.hidden) { return; } // another file was opened meanwhile
          body.textContent = "";
          if (d.note) { body.appendChild(el("p", "note", d.note)); }
          if (d.html) {
            var c = el("div", "");
            c.innerHTML = d.html; // rendered and escaped by rex-tg
            var fd = c.querySelector(".fdiff");
            if (fd) { fd.classList.toggle("wrap", wrapOn); }
            body.appendChild(c);
          }
        })
        .catch(function (e) {
          if (mine !== shown) { return; }
          body.textContent = "";
          body.appendChild(el("p", "note", e.message || "This change could not be loaded."));
        });
    }

    fold.addEventListener("click", function () {
      var anyOpen = !!tree.querySelector(".trow.dir.open");
      tree.querySelectorAll(".trow.dir").forEach(function (r) {
        if (r.classList.contains("open") === anyOpen) { r.click(); }
      });
      syncFold();
    });
    reload.addEventListener("click", refresh);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !v.list.hidden) { refresh(); }
    });
    refresh();
  }

  // --- the control panel ---

  // act makes the id of one tap: 32 random hex digits. The server runs an
  // action at most once per id.
  function act() {
    var b = new Uint8Array(16), s = "";
    crypto.getRandomValues(b);
    for (var i = 0; i < b.length; i++) { s += (b[i] < 16 ? "0" : "") + b[i].toString(16); }
    return s;
  }

  function confirmAsk(msg, yes) {
    if (tg && tg.showConfirm) { tg.showConfirm(msg, function (ok) { if (ok) { yes(); } }); } else if (window.confirm(msg)) { yes(); }
  }

  function openLink(url) {
    if (tg && tg.openTelegramLink) { tg.openTelegramLink(url); } else { window.open(url, "_blank"); }
  }

  function button(text, cls, label) {
    var b = el("button", cls || "btn", text);
    b.type = "button";
    if (label) { b.setAttribute("aria-label", label); }
    return b;
  }

  // acts keeps the act of an action whose outcome is not known (a pending
  // result, or a request that may have reached the server), by the action's
  // key. A tap on the same action sends the same act, so it never runs twice.
  var acts = {};
  function actKey(a) {
    return [a.op, a.pane || "", a.thread || "", a.agent || "", a.path || "", a.name || "", a.on ? "1" : "0"].join("|");
  }

  // post runs one action. The buttons in lock stay off while it runs.
  function post(a, lock) {
    var key = actKey(a);
    a.act = acts[key] || act();
    var all = lock ? Array.prototype.slice.call(lock.querySelectorAll("button")) : [];
    var was = all.map(function (b) { return b.disabled; });
    all.forEach(function (b) { b.disabled = true; });
    if (lock) { lock.setAttribute("aria-busy", "true"); }
    return api("api/control/do?id=" + encodeURIComponent(id), a)
      .catch(function (e) {
        var msg = e.message || "The action failed.";
        // no answer from the server, or a server error: it may have run
        if (!e.status || e.status >= 500) {
          return { ok: false, pending: true, message: msg + " The action may still run." };
        }
        if (e.status === 403 && msg.indexOf("/rt") < 0) { msg += " Open the panel again with /rt."; }
        return { ok: false, message: msg };
      })
      .then(function (r) {
        if (lock) { lock.removeAttribute("aria-busy"); }
        all.forEach(function (b, i) { b.disabled = was[i]; });
        if (r.pending) { acts[key] = a.act; } else { delete acts[key]; }
        return r;
      });
  }

  function resultBox(r) {
    var box = el("div", "result " + (r.pending ? "wait" : r.ok ? "ok" : "bad"));
    box.setAttribute("role", r.ok || r.pending ? "status" : "alert");
    box.appendChild(el("span", "", (r.pending ? "⏳ " : r.ok ? "✓ " : "⚠ ") + (r.message || (r.ok ? "Done." : "The action failed."))));
    if (r.pending) { box.appendChild(el("div", "hint", "Tap again to check; it will not run twice.")); }
    if (r.link) {
      var go = button("↗ Open topic", "btn small");
      go.addEventListener("click", function () { openLink(r.link); });
      box.appendChild(go);
    }
    return box;
  }

  function controlPanel() {
    document.body.classList.remove("fill");
    page.className = "ctl";
    page.textContent = "";

    var state = null, asked = 0, timer = null, inflight = false;
    var ui = {}; // by pane: { edit: "topic"|"pane"|null, msg: result, el: card }

    // the top: pause, time, refresh
    var top = el("div", "ctl-top");
    var pauseLine = el("div", "pauseline");
    var pauseText = el("span", "", "");
    var pauseBtn = button("Pause typing", "btn small");
    pauseLine.appendChild(pauseText);
    pauseLine.appendChild(pauseBtn);
    var upd = el("div", "updline");
    var updText = el("span", "status", "Loading…");
    var reload = button("↻", "chip", "Refresh now");
    upd.appendChild(updText);
    upd.appendChild(reload);
    top.appendChild(pauseLine);
    top.appendChild(upd);
    var topMsg = el("div", "");
    var errBox = el("div", "result bad");
    errBox.hidden = true;
    errBox.setAttribute("role", "alert");
    var agentsHead = el("h2", "sect", "Agents");
    var agentsBox = el("div", "cards");
    var newBox = el("section", "newagent");
    page.appendChild(top);
    page.appendChild(topMsg);
    page.appendChild(errBox);
    page.appendChild(agentsHead);
    page.appendChild(agentsBox);
    page.appendChild(newBox);
    var newPanel = null;

    pauseBtn.addEventListener("click", function () {
      var on = !(state && state.paused);
      function go() {
        post({ op: "pause", on: on }, top).then(function (r) {
          topMsg.textContent = "";
          topMsg.appendChild(resultBox(r));
          refresh();
        });
      }
      if (on) { confirmAsk("Pause typing? Nothing is typed into panes until you resume.", go); } else { go(); }
    });
    reload.addEventListener("click", function () { refresh(); });

    function drawTop() {
      var paused = !!state.paused;
      pauseText.textContent = paused ? "⏸ Typing is paused" : "";
      pauseText.className = paused ? "pausetxt" : "";
      pauseBtn.textContent = paused ? "Resume" : "Pause typing";
      pauseBtn.className = paused ? "btn small primary" : "btn small";
      pauseBtn.setAttribute("aria-label", paused ? "Resume typing into panes" : "Pause typing into panes");
      updText.textContent = "Updated " + (state.at || "");
    }

    function line(cls, text) { return el("div", cls, text); }

    function card(a) {
      var u = ui[a.pane] || (ui[a.pane] = { edit: null, msg: null });
      var c = el("article", "card");
      c.setAttribute("aria-label", a.name || ("Pane " + a.pane));
      var head = el("div", "chead");
      head.appendChild(el("span", "cemoji", a.emoji || "•"));
      head.appendChild(el("span", "cname", a.name || ("Pane " + a.pane)));
      c.appendChild(head);
      var st = a.state || "";
      if (a.since) { st += (st ? " · " : "") + a.since; }
      if (a.mac) { st += (st ? " · " : "") + "💻 on the Mac"; }
      if (st) { c.appendChild(line("cstate", st)); }
      var where = [a.agent, a.folder].filter(Boolean).join(" · ");
      if (where) { c.appendChild(line("cline", where)); }
      if (a.title) { c.appendChild(line("cline muted", a.title)); }
      var rex = [];
      if (a.session) { rex.push("Rex session " + a.session); }
      if (a.label) { rex.push("pane " + a.label); }
      if (rex.length) { c.appendChild(line("cline muted", rex.join(" · "))); }

      var btns = el("div", "btns");
      c.appendChild(btns);
      var form = el("div", "formslot");
      var msg = el("div", "");
      c.appendChild(form);
      c.appendChild(msg);

      function showMsg() {
        msg.textContent = "";
        if (u.msg) { msg.appendChild(resultBox(u.msg)); }
      }
      function done(r, closeEdit) {
        u.msg = r;
        if (r.ok && closeEdit) { u.edit = null; }
        showMsg();
        if (r.ok) { form.textContent = ""; }
        refresh();
      }

      if (a.topic) {
        var t = button("↗ Open topic", "btn primary");
        t.addEventListener("click", function () { openLink(a.topic.link); });
        btns.appendChild(t);
      } else if (a.ready) {
        var o = button("Open topic", "btn primary");
        o.addEventListener("click", function () {
          post({ op: "open", pane: a.pane }, c).then(function (r) { done(r, false); });
        });
        btns.appendChild(o);
      } else {
        btns.appendChild(el("span", "waiting", "Waiting for the session…"));
      }

      // editor: an inline input with Save and Cancel
      function editor(kind) {
        u.edit = kind;
        form.textContent = "";
        var topic = kind === "topic";
        var row = el("div", "editrow");
        var input = el("input", "txt");
        input.type = "text";
        input.maxLength = 100;
        input.value = topic ? a.topic.name : (a.label || a.name || "");
        input.setAttribute("aria-label", topic ? "New topic name" : "New pane name");
        input.autocapitalize = "off";
        input.autocomplete = "off";
        var save = button("Save", "btn small primary");
        var cancel = button("Cancel", "btn small");
        row.appendChild(input);
        row.appendChild(save);
        row.appendChild(cancel);
        form.appendChild(row);
        if (topic) {
          form.appendChild(line("hint", 'Type "auto" to name it after the session again.' + (a.topic.custom ? " Named by you." : "")));
        }
        function close() { u.edit = null; form.textContent = ""; }
        function submit() {
          var v = input.value.trim();
          if (!v) { return; }
          var action = topic ? { op: "rename_topic", thread: a.topic.thread, name: v } : { op: "rename_pane", pane: a.pane, name: v };
          post(action, c).then(function (r) { done(r, true); });
        }
        save.addEventListener("click", submit);
        cancel.addEventListener("click", close);
        input.addEventListener("keydown", function (e) {
          if (e.key === "Enter") { e.preventDefault(); submit(); }
          if (e.key === "Escape") { close(); }
        });
        input.focus();
        input.select();
      }

      if (a.topic) {
        var rt = button("✏️ Rename topic", "btn");
        rt.addEventListener("click", function () { editor("topic"); });
        btns.appendChild(rt);
      }
      var rp = button("✏️ Rename pane", "btn");
      rp.addEventListener("click", function () { editor("pane"); });
      btns.appendChild(rp);
      if (a.topic) {
        var ul = button("🔌 Unlink", "btn danger");
        ul.addEventListener("click", function () {
          confirmAsk("Unlink this session? rex-tg stops mirroring it; the topic stays as history.", function () {
            post({ op: "unlink", thread: a.topic.thread }, c).then(function (r) { done(r, false); });
          });
        });
        btns.appendChild(ul);
      }
      if (a.topic && a.topic.custom) { c.setAttribute("data-custom", "1"); }
      if (u.edit === "topic" && !a.topic) { u.edit = null; }
      if (u.edit) { editor(u.edit); }
      showMsg();
      return c;
    }

    function drawAgents() {
      var seen = {};
      var kids = [];
      state.agents.forEach(function (a) {
        seen[a.pane] = true;
        var u = ui[a.pane];
        // a card with an open input is kept as it is
        if (u && u.edit && u.el && u.el.parentNode === agentsBox) { kids.push(u.el); return; }
        var c = card(a);
        ui[a.pane].el = c;
        kids.push(c);
      });
      Object.keys(ui).forEach(function (k) { if (!seen[k]) { delete ui[k]; } });
      // keep the nodes of open cards in place; replace the others
      var same = kids.length === agentsBox.children.length && kids.every(function (k, i) { return agentsBox.children[i] === k; });
      if (!same) {
        agentsBox.textContent = "";
        kids.forEach(function (k) { agentsBox.appendChild(k); });
      }
      if (!kids.length) { agentsBox.appendChild(el("p", "note empty", "No agents are running in Rex panes.")); }
    }

    // --- new agent ---

    function newAgent() {
      var agent = null, folder = null, hidden = false, list = null, sig = "";
      newBox.textContent = "";
      newBox.appendChild(el("h2", "sect", "New agent"));
      var pickAgent = el("div", "segm");
      pickAgent.setAttribute("role", "radiogroup");
      pickAgent.setAttribute("aria-label", "Agent");
      var chipsBox = el("div", "fchips");
      chipsBox.setAttribute("role", "group");
      chipsBox.setAttribute("aria-label", "Folder shortcuts");
      var chosen = el("div", "chosen");
      var expl = el("div", "explorer");
      var start = button("", "btn primary wide");
      var why = el("div", "hint");
      var res = el("div", "");
      newBox.appendChild(el("div", "label", "Agent"));
      newBox.appendChild(pickAgent);
      newBox.appendChild(el("div", "label", "Folder"));
      newBox.appendChild(chipsBox);
      newBox.appendChild(expl);
      newBox.appendChild(chosen);
      newBox.appendChild(start);
      newBox.appendChild(why);
      newBox.appendChild(res);

      function starters() { return (state && state.starters) || []; }
      function updateStart() {
        var s = starters().filter(function (x) { return x.key === agent; })[0];
        var paused = !!(state && state.paused);
        chosen.textContent = folder ? "Folder: " + folder.shown : "No folder picked yet.";
        start.textContent = s && folder ? "Start " + s.name + " in " + folder.shown : "Start an agent";
        start.disabled = paused || !s || !folder || start.getAttribute("data-busy") === "1";
        why.textContent = paused ? "Typing is paused, so a new agent cannot start. Resume first." : (!s ? "Pick an agent." : (!folder ? "Pick a folder." : ""));
      }
      function drawAgentPick() {
        pickAgent.textContent = "";
        starters().forEach(function (s) {
          var b = button(s.name, "seg-b" + (s.key === agent ? " on" : ""));
          b.setAttribute("role", "radio");
          b.setAttribute("aria-checked", s.key === agent ? "true" : "false");
          b.addEventListener("click", function () { agent = s.key; drawAgentPick(); updateStart(); });
          pickAgent.appendChild(b);
        });
        if (!starters().length) { pickAgent.appendChild(el("span", "note", "No agents can be started.")); }
      }
      function pick(f) {
        folder = { path: f.path, shown: f.shown };
        drawChips();
        updateStart();
      }
      function drawChips() {
        chipsBox.textContent = "";
        ((state && state.folders) || []).forEach(function (f) {
          var b = button("📁 " + f.shown, "chip" + (folder && folder.path === f.path ? " on" : ""));
          b.setAttribute("aria-pressed", folder && folder.path === f.path ? "true" : "false");
          b.addEventListener("click", function () { pick(f); });
          chipsBox.appendChild(b);
        });
      }

      // the folder explorer
      function drawExplorer(l, err) {
        expl.textContent = "";
        var head = el("div", "ehead");
        head.appendChild(el("span", "epath", l ? l.shown : "Folders"));
        if (l && l.git) { head.appendChild(el("span", "gitflag", "🌿 git repository")); }
        expl.appendChild(head);
        var tools = el("div", "etools");
        var hid = button("Show hidden", "chip" + (hidden ? " on" : ""));
        hid.setAttribute("aria-pressed", hidden ? "true" : "false");
        hid.addEventListener("click", function () { hidden = !hidden; load(l ? l.path : (state.home.path)); });
        tools.appendChild(hid);
        if (l) {
          var use = button("Use this folder", "chip primary");
          use.addEventListener("click", function () { pick(l); });
          tools.appendChild(use);
        }
        expl.appendChild(tools);
        if (err) {
          var e = el("div", "result bad", err);
          e.setAttribute("role", "alert");
          expl.appendChild(e);
          return;
        }
        var rows = el("div", "frows");
        if (l.parent) {
          var up = button("↑ Up", "frow up", "Up to the folder above");
          up.addEventListener("click", function () { load(l.parent); });
          rows.appendChild(up);
        }
        l.dirs.forEach(function (d) {
          var r = el("button", "frow");
          r.type = "button";
          r.appendChild(el("span", "ic", "📁"));
          r.appendChild(el("span", "nm", d.shown));
          r.appendChild(el("span", "chev", "›"));
          r.setAttribute("aria-label", "Open folder " + d.shown);
          r.addEventListener("click", function () { load(d.path); });
          rows.appendChild(r);
        });
        if (!l.dirs.length) { rows.appendChild(el("div", "tnote", "No folders here.")); }
        if (l.more) { rows.appendChild(el("div", "tnote", "… more folders not shown")); }
        expl.appendChild(rows);
      }
      var loading = 0;
      function load(path) {
        var mine = ++loading;
        expl.setAttribute("aria-busy", "true");
        api("api/control/folders?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(path || "") + (hidden ? "&hidden=1" : ""))
          .then(function (l) { if (mine === loading) { list = l; drawExplorer(l); } })
          .catch(function (e) { if (mine === loading) { drawExplorer(list, e.message || "This folder could not be loaded."); } })
          .then(function () { if (mine === loading) { expl.removeAttribute("aria-busy"); } });
      }

      start.addEventListener("click", function () {
        var s = starters().filter(function (x) { return x.key === agent; })[0];
        if (!s || !folder) { return; }
        start.setAttribute("data-busy", "1");
        post({ op: "new", agent: s.key, path: folder.path }, newBox).then(function (r) {
          start.removeAttribute("data-busy");
          res.textContent = "";
          var box = resultBox(r);
          if (r.ok) { box.appendChild(el("div", "hint", "Progress continues in the main chat.")); }
          res.appendChild(box);
          updateStart();
          refresh();
        });
      });

      return {
        // update takes a new state: it redraws only what changed, so the form stays as it is
        update: function () {
          var s = JSON.stringify([state.starters, state.folders]);
          if (!agent || !starters().some(function (x) { return x.key === agent; })) { agent = starters().length ? starters()[0].key : null; }
          if (s !== sig) {
            sig = s;
            drawAgentPick();
            drawChips();
          }
          if (!list && !loading) { load(state.home.path); }
          updateStart();
        }
      };
    }

    function render() {
      drawTop();
      drawAgents();
      if (!newPanel) { newPanel = newAgent(); }
      newPanel.update();
    }

    function refresh() {
      clearTimeout(timer);
      var mine = ++asked;
      return api("api/control?id=" + encodeURIComponent(id))
        .then(function (s) {
          if (mine !== asked) { return; }
          state = s;
          state.agents = s.agents || [];
          errBox.hidden = true;
          render();
        })
        .catch(function (e) {
          if (mine !== asked) { return; }
          errBox.textContent = (e.message || "Could not read the agents.") + " Trying again soon.";
          errBox.hidden = false;
          if (!state) { updText.textContent = "Not loaded"; }
        })
        .then(function () { if (mine === asked) { schedule(); } });
    }
    function schedule() {
      clearTimeout(timer);
      timer = setTimeout(function () {
        if (document.hidden) { schedule(); } else { refresh(); }
      }, 5000);
    }
    document.addEventListener("visibilitychange", function () { if (!document.hidden) { refresh(); } });
    refresh();
  }

  // --- the page ---

  api("api/page?id=" + encodeURIComponent(id))
    .then(function (p) {
      setTitle(p.title);
      if (p.kind === "screen") { liveScreen(); return; }
      if (p.kind === "files") { fileBrowser(p.title || "Files"); return; }
      if (p.kind === "changes") { changesView(); return; }
      if (p.kind === "control") { controlPanel(); return; }
      page.className = "";
      page.innerHTML = p.html; // rendered and escaped by rex-tg
    })
    .catch(function (e) {
      note(e.message || "This page could not be loaded.");
    });
})();
