// rex-tg Mini App: shows one page: an answer, the changes of a turn, the
// live screen of a topic's pane, the files of the agent's folder, the
// changes of its repository, or the control panel (the agents in Rex panes:
// open, rename, start, unlink; their screen, files and changes).
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
  var params = new URLSearchParams(location.search);
  var id = params.get("p") || "";
  var goView = params.get("go") || "";
  var initData = tg ? tg.initData : "";
  // The keyboard's buttons open this page with a key (k): it authorizes reads
  // and validated control-panel actions, and is kept for pages that follow.
  var key = params.get("k") || "";
  if (key) {
    try { sessionStorage.setItem("rex-tg.key", key); } catch (e) { /* the key stays in the links */ }
  } else {
    try { key = sessionStorage.getItem("rex-tg.key") || ""; } catch (e) { key = ""; }
  }
  var keyMode = !initData && !!key;
  if (!initData && !key) {
    page.textContent = "Open this page from its button in Telegram.";
    return;
  }

  // appURL is the address of a page of this app (q: p, from, go, t), with the key.
  function appURL(q) {
    var s = [];
    if (key) { s.push("k=" + encodeURIComponent(key)); }
    ["p", "from", "go", "t"].forEach(function (n) {
      if (q[n] !== undefined && q[n] !== "") { s.push(n + "=" + encodeURIComponent(q[n])); }
    });
    return "app?" + s.join("&") + location.hash;
  }

  // api reads path; with a body it posts it as JSON. A read (no body) with
  // wait stops after wait ms; a post is never stopped, so an action does not
  // look failed while it runs.
  function api(path, body, wait) {
    var opt = { headers: keyMode ? { "X-Rex-Key": key } : { "X-Telegram-Init-Data": initData } };
    var timer = null;
    if (body !== undefined) {
      opt.method = "POST";
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    } else if (wait && window.AbortController) {
      var ac = new AbortController();
      opt.signal = ac.signal;
      timer = setTimeout(function () { ac.abort(); }, wait);
    }
    // The timer runs until the body is read, and stops on every path.
    return fetch(path, opt).then(function (r) {
      return r.json().catch(function (e) {
        if (e && e.name === "AbortError") { throw e; }
        return null;
      }).then(function (body) {
        if (!r.ok || !body) {
          var err = new Error((body && body.error) || ("HTTP " + r.status));
          err.status = r.status;
          throw err;
        }
        return body;
      });
    }).then(function (body) {
      clearTimeout(timer);
      return body;
    }, function (e) {
      clearTimeout(timer);
      // Only the timer above aborts a request.
      if (e && e.name === "AbortError") { throw new Error("The Mac did not answer in time. Check the connection, then try again."); }
      throw e;
    });
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    if (text !== undefined) { e.textContent = text; }
    return e;
  }

  // scrollTables puts each table of rendered text in its own box that
  // scrolls sideways, and each cell's text in a block that caps the
  // column's width (a cell itself takes no max-width). See .tscroll.
  function scrollTables(root) {
    var tables = root.querySelectorAll("table");
    for (var i = 0; i < tables.length; i++) {
      var t = tables[i];
      if (t.parentNode.classList.contains("tscroll")) { continue; }
      var box = el("div", "tscroll");
      t.parentNode.insertBefore(box, t);
      box.appendChild(t);
      var cells = t.querySelectorAll("th, td");
      for (var j = 0; j < cells.length; j++) {
        var c = el("div", "cell");
        while (cells[j].firstChild) { c.appendChild(cells[j].firstChild); }
        cells[j].appendChild(c);
      }
    }
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
  // rootBack is what Back does at the top of a page opened from the panel:
  // it returns to the panel.
  var backAction = null, rootBack = null;
  if (tg && tg.BackButton) { tg.BackButton.onClick(function () { if (backAction) { backAction(); } }); }
  function setBack(f) {
    backAction = f || rootBack;
    if (tg && tg.BackButton) { if (backAction) { tg.BackButton.show(); } else { tg.BackButton.hide(); } }
  }
  function backToPanel() {
    if (history.length > 1) { history.back(); } else if (tg && tg.close) { tg.close(); }
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
            scrollTables(c);
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

  // hap gives a haptic tick: "light" for a tap, or a result kind.
  function hap(kind) {
    try {
      var h = tg && tg.HapticFeedback;
      if (!h) { return; }
      if (kind === "success" || kind === "error" || kind === "warning") { h.notificationOccurred(kind); } else { h.impactOccurred("light"); }
    } catch (e) { /* an older Telegram: no haptics */ }
  }

  // confirmAsk asks in Telegram's popup, or in showConfirm, or in the browser.
  function confirmAsk(o, yes) {
    if (tg && tg.showPopup && (!tg.isVersionAtLeast || tg.isVersionAtLeast("6.2"))) {
      try {
        tg.showPopup({
          title: o.title, message: o.message,
          buttons: [{ id: "ok", type: o.danger ? "destructive" : "default", text: o.ok }, { type: "cancel" }]
        }, function (bid) { if (bid === "ok") { yes(); } });
        return;
      } catch (e) { /* an older Telegram */ }
    }
    if (tg && tg.showConfirm) {
      try { tg.showConfirm(o.message, function (ok) { if (ok) { yes(); } }); return; } catch (e) { /* an older Telegram */ }
    }
    if (window.confirm(o.message)) { yes(); }
  }

  // openLink goes to a topic. Telegram keeps the Mini App open after
  // openTelegramLink, so the topic would open behind the panel and the tap
  // would seem to do nothing: the panel closes right after.
  function openLink(url) {
    if (tg && tg.openTelegramLink) {
      tg.openTelegramLink(url);
      if (tg.close) { setTimeout(function () { tg.close(); }, 60); }
    } else {
      window.open(url, "_blank");
    }
  }

  function openAgentTopic(pane, lock, onFailure) {
    hap("light");
    post({ op: "open", pane: pane }, lock).then(function (r) {
      if (r.ok && r.link) { openLink(r.link); return; }
      toast(r);
      if (onFailure) { onFailure(); }
    });
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
  // An entry lives for 10 minutes, and ends when any action on the same
  // target (pane or topic) gets a final result: after that the old act would
  // only make the server return a stale result for an action it never ran.
  var acts = {}, actTTL = 10 * 60 * 1000;
  function actTarget(a) { return a.pane ? "p" + a.pane : a.thread ? "t" + a.thread : a.op; }
  function dropActs(target) {
    Object.keys(acts).forEach(function (k) { if (acts[k].target === target) { delete acts[k]; } });
  }
  function actKey(a) {
    return [a.op, a.pane || "", a.thread || "", a.agent || "", a.path || "", a.name || "", a.kind || "", a.on ? "1" : "0"].join("|");
  }

  // post runs one action. The buttons in lock stay off while it runs.
  function post(a, lock) {
    var key = actKey(a);
    var target = actTarget(a), old = acts[key];
    if (old && Date.now() - old.at > actTTL) { delete acts[key]; old = null; }
    a.act = old ? old.act : act();
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
        if (r.pending) { acts[key] = { act: a.act, at: old ? old.at : Date.now(), target: target }; } else { dropActs(target); delete acts[key]; }
        return r;
      });
  }

  // toast shows a result without blocking the page.
  var toastBox = null;
  function toast(r, extra) {
    if (!toastBox) {
      toastBox = el("div", "toasts");
      document.body.appendChild(toastBox);
    }
    hap(r.pending ? "warning" : r.ok ? "success" : "error");
    var t = el("div", "toast " + (r.pending ? "wait" : r.ok ? "ok" : "bad"));
    t.setAttribute("role", r.ok || r.pending ? "status" : "alert");
    var body = el("div", "tbody");
    body.appendChild(el("div", "", (r.pending ? "⏳ " : r.ok ? "✓ " : "⚠ ") + (r.message || (r.ok ? "Done." : "The action failed."))));
    if (r.pending) { body.appendChild(el("div", "thint", "Tap again to check; it will not run twice.")); }
    if (r.ok && extra) { body.appendChild(el("div", "thint", extra)); }
    t.appendChild(body);
    if (r.link) {
      var go = button("↗ Open", "btn small");
      go.addEventListener("click", function () { openLink(r.link); });
      t.appendChild(go);
    }
    var x = button("✕", "tx", "Dismiss");
    t.appendChild(x);
    function gone() { if (t.parentNode) { t.parentNode.removeChild(t); } }
    x.addEventListener("click", gone);
    toastBox.appendChild(t);
    while (toastBox.children.length > 3) { toastBox.removeChild(toastBox.firstChild); }
    setTimeout(gone, r.ok && !r.link ? 4500 : 9000);
  }

  // goPage opens another Mini App page of the bot; Back returns to the panel.
  function goPage(p) {
    try { if (tg && tg.MainButton) { tg.MainButton.hide(); } } catch (e) { /* none */ }
    location.href = appURL({ p: p, from: "panel" });
  }

  // The kind of a state, for its colour and its filter chip.
  function stateKind(s) {
    s = (s || "").toLowerCase();
    if (/^waiting/.test(s)) { return "waiting"; }
    if (/err|fail|stuck|offline|dead|crash/.test(s)) { return "bad"; }
    if (/work|run|busy|think|start/.test(s)) { return "working"; }
    if (/idle|ready|done/.test(s)) { return "idle"; }
    return "other";
  }
  var kindOrder = ["waiting", "working", "idle", "bad", "other"];
  var kindName = { waiting: "waiting", working: "working", idle: "idle", bad: "attention", other: "other" };

  function controlPanel() {
    document.body.classList.remove("fill");
    document.body.classList.add("ctlbody");
    page.className = "ctl";
    page.textContent = "";
    var header = document.querySelector("header.top");
    header.classList.add("stack");
    // The panel draws its own start bar (see .startbar), not Telegram's
    // MainButton: Telegram's bottom bar is see-through on iOS, and the page
    // showed through it dimmed, like a stray dark shape.
    try { if (tg && tg.MainButton) { tg.MainButton.hide(); } } catch (e) { /* none */ }

    // Telegram's safe areas (the notch, the home indicator), when it reports them.
    function insets() {
      try {
        var si = tg.safeAreaInset || {}, ci = tg.contentSafeAreaInset || {};
        ["top", "bottom", "left", "right"].forEach(function (k) {
          root.style.setProperty("--sai-" + k, ((si[k] || 0) + (ci[k] || 0)) + "px");
        });
      } catch (e) { /* an older Telegram: the page's own padding */ }
    }
    insets();
    if (tg && tg.onEvent) { ["safeAreaChanged", "contentSafeAreaChanged"].forEach(function (ev) { tg.onEvent(ev, insets); }); }

    var state = null, asked = 0, timer = null;
    var tab = load("rex-tg.panel.tab");
    if (tab !== "new" && tab !== "topics") { tab = "agents"; }
    var scrollAt = {}; // the scroll position of each tab
    var group = load("rex-tg.panel.group") || "none";
    if (["none", "state", "folder", "session"].indexOf(group) < 0) { group = "none"; }
    var filters = {}, query = "";
    var open = {};  // the rows that are expanded, by pane
    var starts = []; // agents being started: { block, label, at }
    var pulse = null; // the pane of a new agent to show
    var cards = {}; // by pane: { sig, el }
    var sheet = null, newPanel = null;

    // --- the sticky head: paused banner, tabs, state filters ---
    var head = el("div", "ctl-head");
    var banner = el("div", "banner", "⏸ Typing is paused. Send /unpause in the chat to allow it again.");
    banner.hidden = true;
    banner.setAttribute("role", "status");
    var seg = el("div", "segc");
    seg.setAttribute("role", "tablist");
    seg.setAttribute("aria-label", "Panel");
    var tabAgents = button("Agents", "", "");
    var tabTopics = button("Topics", "", "");
    var tabNew = button("New agent", "", "");
    var count = el("span", "cnt", "");
    var tcount = el("span", "cnt", "");
    tabAgents.appendChild(count);
    tabTopics.appendChild(tcount);
    [tabAgents, tabTopics, tabNew].forEach(function (b) { b.setAttribute("role", "tab"); seg.appendChild(b); });
    var chipsRow = el("div", "schips");
    chipsRow.setAttribute("role", "group");
    chipsRow.setAttribute("aria-label", "Filter by state");
    head.appendChild(banner);
    head.appendChild(seg);
    head.appendChild(chipsRow);
    header.appendChild(head);

    var errBar = el("div", "errbar", "");
    errBar.hidden = true;
    errBar.setAttribute("role", "alert");
    var agentsView = el("div", "view-agents");
    var topicsView = el("div", "view-topics");
    var newView = el("div", "view-new");
    page.appendChild(errBar);
    page.appendChild(agentsView);
    page.appendChild(topicsView);
    page.appendChild(newView);

    // --- the Agents tab: tools, then the list ---
    var tools = el("div", "tools2");
    var search = el("input", "txt");
    search.type = "search";
    search.placeholder = "Search name, folder or agent";
    search.setAttribute("aria-label", "Search agents");
    search.autocomplete = "off";
    var reload = button("↻", "ibtn", "Refresh now");
    var groupSel = el("select", "sel");
    groupSel.setAttribute("aria-label", "Group agents");
    [["none", "No grouping"], ["state", "Group by state"], ["folder", "Group by folder"], ["session", "Group by Rex session"]].forEach(function (o) {
      var op = el("option", "", o[1]);
      op.value = o[0];
      groupSel.appendChild(op);
    });
    groupSel.value = group;
    var expandAll = button("Expand all", "chip");
    expandAll.hidden = true;
    var row1 = el("div", "trow2");
    row1.appendChild(search);
    row1.appendChild(reload);
    var row2 = el("div", "trow2");
    row2.appendChild(groupSel);
    row2.appendChild(expandAll);
    tools.appendChild(row1);
    tools.appendChild(row2);
    var cardsBox = el("div", "cards");
    var foot = el("div", "foot", "");
    agentsView.appendChild(tools);
    agentsView.appendChild(cardsBox);
    agentsView.appendChild(foot);
    // the first look: grey blocks until the state arrives
    for (var i = 0; i < 3; i++) { cardsBox.appendChild(el("div", "skel card-skel")); }

    search.addEventListener("input", function () { query = search.value.trim().toLowerCase(); drawAgents(); });
    reload.addEventListener("click", function () { hap("light"); refresh(); });
    groupSel.addEventListener("change", function () { group = groupSel.value; save("rex-tg.panel.group", group === "none" ? null : group); drawAgents(); });
    expandAll.addEventListener("click", function () {
      hap("light");
      var shown = visible();
      var anyClosed = shown.some(function (a) { return !open[a.pane]; });
      shown.forEach(function (a) { if (anyClosed) { open[a.pane] = true; } else { delete open[a.pane]; } });
      Object.keys(cards).forEach(function (k) { applyOpen(cards[k].el, k); });
      syncExpandAll();
    });

    function syncExpandAll() {
      var shown = visible();
      var anyClosed = shown.some(function (a) { return !open[a.pane]; });
      expandAll.textContent = anyClosed ? "Expand all" : "Collapse all";
      expandAll.hidden = !shown.length;
    }

    // --- tabs and the Telegram buttons ---
    var tabBtns = [[tabAgents, "agents"], [tabTopics, "topics"], [tabNew, "new"]];
    function setTab(t) {
      if (t !== tab) { scrollAt[tab] = window.scrollY; }
      var changed = t !== tab;
      tab = t;
      save("rex-tg.panel.tab", t === "agents" ? null : t);
      agentsView.hidden = t !== "agents";
      topicsView.hidden = t !== "topics";
      newView.hidden = t !== "new";
      chipsRow.hidden = t !== "agents" || !state || !state.agents.length;
      tabBtns.forEach(function (p) {
        p[0].setAttribute("aria-selected", p[1] === t ? "true" : "false");
        p[0].className = p[1] === t ? "on" : "";
        p[0].tabIndex = p[1] === t ? 0 : -1;
      });
      if (t === "new" && newPanel) { newPanel.shown(); }
      document.body.classList.toggle("on-new", t === "new");
      syncBack();
      if (changed) { window.scrollTo(0, scrollAt[t] || 0); }
    }
    tabBtns.forEach(function (p) { p[0].addEventListener("click", function () { hap("light"); setTab(p[1]); }); });
    seg.addEventListener("keydown", function (e) {
      var d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (!d) { return; }
      var i = 0;
      tabBtns.forEach(function (p, k) { if (p[1] === tab) { i = k; } });
      var n = tabBtns[(i + d + tabBtns.length) % tabBtns.length];
      setTab(n[1]);
      n[0].focus();
    });
    window.addEventListener("pageshow", function (e) { if (e.persisted) { refresh(); } });

    // Back closes a sheet, goes up a folder, or returns to the Agents tab.
    function syncBack() {
      var f = null;
      if (sheet) { f = function () { sheet.close(); }; }
      else if (tab === "new") { f = newPanel && newPanel.canUp() ? newPanel.up : function () { setTab("agents"); }; }
      else if (tab === "topics") { f = function () { setTab("agents"); }; }
      setBack(f);
    }

    // --- sheets ---
    function openSheet(label, build) {
      if (sheet) { sheet.close(); }
      var prev = document.activeElement;
      var scrim = el("div", "scrim");
      var box = el("div", "sheet");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-modal", "true");
      box.setAttribute("aria-label", label);
      box.appendChild(el("div", "grip"));
      var h = el("h3", "stitle", label);
      box.appendChild(h);
      scrim.appendChild(box);
      function onKey(e) {
        if (e.key === "Escape") { s.close(); return; }
        if (e.key !== "Tab") { return; }
        var f = Array.prototype.slice.call(box.querySelectorAll("button:not(:disabled), input, select"));
        if (!f.length) { return; }
        var first = f[0], last = f[f.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
      var s = {
        el: box,
        close: function () {
          if (!scrim.parentNode) { return; }
          scrim.parentNode.removeChild(scrim);
          document.removeEventListener("keydown", onKey);
          document.body.style.overflow = "";
          if (sheet === s) { sheet = null; }
          if (prev && prev.focus) { try { prev.focus(); } catch (e) { /* gone */ } }
          syncBack();
        }
      };
      scrim.addEventListener("click", function (e) { if (e.target === scrim) { s.close(); } });
      document.addEventListener("keydown", onKey);
      build(box, s.close);
      document.body.appendChild(scrim);
      document.body.style.overflow = "hidden";
      sheet = s;
      syncBack();
      var first = box.querySelector("input") || box.querySelector("button");
      if (first) { first.focus(); }
    }

    function renameSheet(a) {
      openSheet("Rename " + (a.name || "agent"), function (box, close) {
        box.appendChild(el("p", "shint", "Renames the topic, the Rex pane and its tab." + (a.topic && a.topic.custom ? " Named by you." : "")));
        var input = el("input", "txt big");
        input.type = "text";
        input.maxLength = 100;
        input.value = (a.topic && a.topic.name) || a.name || "";
        input.setAttribute("aria-label", "New name");
        input.autocapitalize = "off";
        input.autocomplete = "off";
        box.appendChild(input);
        var row = el("div", "srow");
        var save = button("Save", "btn primary");
        var auto = button("Auto name", "btn", "Name it after the session again");
        var cancel = button("Cancel", "btn");
        row.appendChild(save);
        row.appendChild(auto);
        row.appendChild(cancel);
        box.appendChild(row);
        box.appendChild(el("p", "shint", "Auto name names it after the session again."));
        var prefill = input.value.trim();
        function changed() { save.disabled = !input.value.trim() || input.value.trim() === prefill; }
        changed();
        input.addEventListener("input", changed);
        function submit(name) {
          name = name.trim();
          if (!name || (name !== "auto" && name === prefill)) { input.focus(); return; }
          hap("light");
          post({ op: "rename", pane: a.pane, name: name }, box).then(function (r) {
            toast(r);
            if (r.ok) { close(); }
            refresh();
          });
        }
        save.addEventListener("click", function () { submit(input.value); });
        auto.addEventListener("click", function () { submit("auto"); });
        cancel.addEventListener("click", close);
        input.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); if (!save.disabled) { submit(input.value); } } });
        setTimeout(function () { input.select(); }, 0);
      });
    }

    // --- the head: banner, count, state chips ---
    function drawHead() {
      banner.hidden = !state.paused;
      count.textContent = String(state.agents.length);
      var nt = (state.topics || []).length;
      tcount.textContent = String(nt);
      tcount.hidden = !nt;
      tabTopics.setAttribute("aria-label", "Topics, " + nt);
      count.hidden = !state.agents.length;
      tabAgents.setAttribute("aria-label", "Agents, " + state.agents.length);
      var n = {};
      state.agents.forEach(function (a) { var k = stateKind(a.state); n[k] = (n[k] || 0) + 1; });
      Object.keys(filters).forEach(function (k) { if (!n[k]) { delete filters[k]; } });
      chipsRow.textContent = "";
      kindOrder.forEach(function (k) {
        if (!n[k]) { return; }
        var on = !!filters[k];
        var b = button("", "sc k-" + k + (on ? " on" : ""));
        b.appendChild(el("i", "dot", ""));
        b.appendChild(document.createTextNode(n[k] + " " + kindName[k]));
        b.setAttribute("aria-pressed", on ? "true" : "false");
        b.setAttribute("aria-label", n[k] + " " + kindName[k] + (on ? ", filter on" : ", filter"));
        b.addEventListener("click", function () { hap("light"); if (filters[k]) { delete filters[k]; } else { filters[k] = true; } drawHead(); drawAgents(); });
        chipsRow.appendChild(b);
      });
      chipsRow.hidden = tab !== "agents" || !state.agents.length;
    }

    // --- the agent rows ---
    function match(a) {
      var any = Object.keys(filters).length > 0;
      if (any && !filters[stateKind(a.state)]) { return false; }
      if (query) {
        var hay = [a.name, a.folder, a.agent, a.title, a.session, a.label].join(" ").toLowerCase();
        if (hay.indexOf(query) < 0) { return false; }
      }
      return true;
    }
    function visible() { return state ? state.agents.filter(match) : []; }

    function rexMap(session, tabName, pane) { return [session, tabName, pane].filter(Boolean).join(" › "); }
    function tag(text, cls) { return el("span", "tag" + (cls ? " " + cls : ""), text); }

    function applyOpen(c, pane) {
      var on = !!open[pane];
      var rowBtn = c.querySelector(".rowmain");
      var det = c.querySelector(".cdetail");
      if (!rowBtn || !det) { return; }
      rowBtn.setAttribute("aria-expanded", on ? "true" : "false");
      det.hidden = !on;
      c.classList.toggle("open", on);
    }

    function card(a) {
      var kind = stateKind(a.state);
      var c = el("article", "card k-" + kind);
      var nm = a.name || ("Pane " + a.pane);
      var did = "d" + a.pane;

      // the compact row: a button that opens the details, and a topic shortcut
      var top = el("div", "ctop");
      var rowBtn = el("button", "rowmain");
      rowBtn.type = "button";
      rowBtn.setAttribute("aria-controls", did);
      rowBtn.setAttribute("aria-expanded", "false");
      rowBtn.appendChild(el("span", "cemoji", a.emoji || "•"));
      var mid = el("span", "cmid");
      mid.appendChild(el("span", "cname", nm));
      var st = a.state || "";
      if (a.since) { st += (st ? " · " : "") + a.since; }
      mid.appendChild(el("span", "cstate", st));
      rowBtn.appendChild(mid);
      rowBtn.appendChild(el("span", "chev", "›"));
      top.appendChild(rowBtn);
      if (a.topic || a.ready) {
        var sc = button(a.topic ? "↗" : "＋", "ibtn small", a.topic ? "Open or reconnect the topic of " + nm : "Connect " + nm + " to Telegram in a new topic");
        sc.addEventListener("click", function () { openAgentTopic(a.pane, c, refresh); });
        top.appendChild(sc);
      }
      c.appendChild(top);

      // the details
      var det = el("div", "cdetail");
      det.id = did;
      det.setAttribute("role", "region");
      det.setAttribute("aria-label", "Details of " + nm);
      det.hidden = true;
      var where = [a.agent, a.folder].filter(Boolean).join(" · ");
      if (where) { det.appendChild(el("div", "cmeta", where)); }
      if (a.title) { det.appendChild(el("div", "ctitle", a.title)); }
      var map = rexMap(a.session, a.tab, a.label);
      if (map) { det.appendChild(el("div", "cmap", "Rex: " + map)); }
      var tags = el("div", "tags");
      if (a.mac) { tags.appendChild(tag("💻 on the Mac")); }
      if (a.topic && a.topic.status === "offline") { tags.appendChild(tag("topic offline", "warn")); }
      if (tags.children.length) { det.appendChild(tags); }

      var acts2 = el("div", "cact");
      if (a.topic) {
        var t = button("↗ Open / reconnect", "btn primary", "Open or reconnect the topic of " + nm);
        t.addEventListener("click", function () { openAgentTopic(a.pane, c, refresh); });
        acts2.appendChild(t);
      } else if (a.ready) {
        var o = button("＋ Connect to Telegram", "btn primary", "Connect " + nm + " to Telegram in a new topic");
        o.addEventListener("click", function () { openAgentTopic(a.pane, c, refresh); });
        acts2.appendChild(o);
      } else {
        acts2.appendChild(el("span", "waiting", "Waiting for the session…"));
      }
      function pageBtn(icon, text, kindKey) {
        var b = button(icon + " " + text, "btn", text + " of " + nm);
        b.addEventListener("click", function () {
          hap("light");
          post({ op: "page", pane: a.pane, kind: kindKey }, c).then(function (r) {
            if (r.page) { goPage(r.page); return; }
            toast(r);
            refresh();
          });
        });
        acts2.appendChild(b);
      }
      if (a.topic) {
        pageBtn("🖥", "Screen", "screen");
        if (state.files) { pageBtn("📂", "Files", "files"); }
        if (state.changes) { pageBtn("📝", "Changes", "changes"); }
      }
      var rn = button("✏️ Rename", "btn", "Rename " + nm);
      rn.addEventListener("click", function () { hap("light"); renameSheet(a); });
      acts2.appendChild(rn);
      if (a.topic) {
        var ul = button("🔌 Unlink", "btn danger", "Unlink " + nm);
        ul.addEventListener("click", function () {
          hap("light");
          confirmAsk({ title: "Unlink this session?", message: "rex-tg stops mirroring this session; the topic stays as history.", ok: "Unlink", danger: true }, function () {
            post({ op: "unlink", thread: a.topic.thread }, c).then(function (r) { toast(r); refresh(); });
          });
        });
        acts2.appendChild(ul);
      }
      det.appendChild(acts2);
      c.appendChild(det);

      rowBtn.addEventListener("click", function () {
        hap("light");
        if (open[a.pane]) { delete open[a.pane]; } else { open[a.pane] = true; }
        applyOpen(c, a.pane);
        syncExpandAll();
      });
      applyOpen(c, a.pane);
      return c;
    }

    function groupOf(a) {
      if (group === "state") { var k = stateKind(a.state); return { key: k, label: kindName[k], order: kindOrder.indexOf(k) }; }
      if (group === "folder") { return { key: a.folder || "", label: a.folder || "No folder", order: 0 }; }
      return { key: a.session || "", label: a.session ? "Rex session " + a.session : "No Rex session", order: 0 };
    }

    // startRow is the temporary row of an agent that is being started.
    function startRow(sx) {
      var lost = Date.now() - sx.at > 120000;
      var c = el("div", "card starting" + (lost ? " lost" : ""));
      c.setAttribute("role", "status");
      var t = el("div", "stext");
      t.appendChild(el("div", "cname", lost ? "Not seen yet — check the main chat" : "⏳ Starting " + sx.label + "…"));
      if (lost) { t.appendChild(el("div", "cstate", sx.label)); }
      c.appendChild(t);
      if (lost) {
        var x = button("✕", "ibtn small", "Dismiss: " + sx.label);
        x.addEventListener("click", function () { starts = starts.filter(function (o) { return o !== sx; }); drawAgents(); });
        c.appendChild(x);
      }
      return c;
    }

    // matchStarts ends the starting row of an agent that now shows in the list.
    function matchStarts() {
      starts = starts.filter(function (sx) {
        var found = state.agents.filter(function (a) { return a.block && a.block === sx.block; })[0];
        if (!found) { return true; }
        if (!match(found)) { filters = {}; query = ""; search.value = ""; drawHead(); }
        open[found.pane] = true;
        pulse = found.pane;
        return false;
      });
    }

    // showNew scrolls to the new agent, opens it and pulses it for a moment.
    function showNew() {
      if (pulse === null || tab !== "agents") { return; }
      var cd = cards[pulse], pane = pulse;
      if (!cd) { return; }
      pulse = null;
      applyOpen(cd.el, pane);
      var calm = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      try { cd.el.scrollIntoView({ block: "center", behavior: calm ? "auto" : "smooth" }); } catch (e) { cd.el.scrollIntoView(); }
      cd.el.classList.add("pulse");
      setTimeout(function () { cd.el.classList.remove("pulse"); }, 4500);
    }

    function afterStart(r, label) {
      if (!r.ok) { return; }
      filters = {};
      query = "";
      search.value = "";
      if (r.block && !starts.some(function (o) { return o.block === r.block; })) { starts.push({ block: r.block, label: label, at: Date.now() }); }
      setTab("agents");
      drawHead();
      drawAgents();
      window.scrollTo(0, 0);
    }

    function drawAgents() {
      var list = visible();
      var seen = {};
      state.agents.forEach(function (a) { seen[a.pane] = true; });
      Object.keys(cards).forEach(function (k) { if (!seen[k]) { delete cards[k]; delete open[k]; } });
      var nodes = [];
      function cardFor(a) {
        var calm = {};
        Object.keys(a).forEach(function (k) { if (k !== "since") { calm[k] = a[k]; } });
        var sig = JSON.stringify([calm, state.files, state.changes]);
        var cd = cards[a.pane];
        // a card whose action runs is kept until it returns
        var busy = cd && cd.el.getAttribute("aria-busy") === "true";
        if (!cd || (cd.sig !== sig && !busy)) { cd = cards[a.pane] = { sig: sig, el: card(a) }; }
        else if (!busy) {
          // only the age changed: update its text in place
          var stEl = cd.el.querySelector(".cstate");
          var st = a.state || "";
          if (a.since) { st += (st ? " · " : "") + a.since; }
          if (stEl && stEl.textContent !== st) { stEl.textContent = st; }
        }
        return cd.el;
      }
      if (group === "none") {
        list.forEach(function (a) { nodes.push(cardFor(a)); });
      } else {
        var gs = [], idx = {};
        list.forEach(function (a) {
          var g = groupOf(a);
          if (idx[g.key] === undefined) { idx[g.key] = gs.length; gs.push({ label: g.label, order: g.order, items: [] }); }
          gs[idx[g.key]].items.push(a);
        });
        if (group === "state") { gs.sort(function (x, y) { return x.order - y.order; }); }
        gs.forEach(function (g) {
          var h = el("h3", "ghead");
          h.appendChild(el("span", "", g.label));
          h.appendChild(el("span", "gcount", String(g.items.length)));
          nodes.push(h);
          g.items.forEach(function (a) { nodes.push(cardFor(a)); });
        });
      }
      var keep = document.activeElement && cardsBox.contains(document.activeElement) ? document.activeElement : null;
      cardsBox.textContent = "";
      starts.forEach(function (sx) { cardsBox.appendChild(startRow(sx)); });
      if (!state.agents.length && !starts.length) {
        var e = el("div", "empty");
        e.appendChild(el("div", "eico", "🛰"));
        e.appendChild(el("div", "etitle", "No agents are running in Rex panes."));
        e.appendChild(el("div", "ehint", "Start one from the New agent tab."));
        var go = button("New agent", "btn primary");
        go.addEventListener("click", function () { setTab("new"); });
        e.appendChild(go);
        cardsBox.appendChild(e);
      } else if (!list.length && !state.agents.length) {
        // only the starting rows
      } else if (!list.length) {
        var e2 = el("div", "empty");
        e2.appendChild(el("div", "etitle", "No agent matches."));
        var clear = button("Clear filters", "btn");
        clear.addEventListener("click", function () { filters = {}; query = ""; search.value = ""; drawHead(); drawAgents(); });
        e2.appendChild(clear);
        cardsBox.appendChild(e2);
      } else {
        nodes.forEach(function (n) { cardsBox.appendChild(n); });
      }
      if (keep && keep.isConnected) { try { keep.focus({ preventScroll: true }); } catch (e) { /* gone */ } }
      showNew();
      syncExpandAll();
      foot.textContent = "Updated " + (state.at || "") + " · " + list.length + " of " + state.agents.length + " shown";
    }

    // --- the Topics tab: every topic, with the Rex session, tab and pane of its agent ---
    var tfilters = {}, tquery = "", topen = {}, tcards = {};
    var ttools = el("div", "tools2");
    var tsearch = el("input", "txt");
    tsearch.type = "search";
    tsearch.placeholder = "Search topic, folder or agent";
    tsearch.setAttribute("aria-label", "Search topics");
    tsearch.autocomplete = "off";
    var treload = button("↻", "ibtn", "Refresh now");
    var trow1 = el("div", "trow2");
    trow1.appendChild(tsearch);
    trow1.appendChild(treload);
    var tchips = el("div", "schips wrapchips");
    tchips.setAttribute("role", "group");
    tchips.setAttribute("aria-label", "Filter topics");
    var cleanBtn = button("", "btn small danger");
    cleanBtn.hidden = true;
    var trow2 = el("div", "trow2");
    trow2.appendChild(tchips);
    ttools.appendChild(trow1);
    ttools.appendChild(trow2);
    var tbox = el("div", "cards");
    var tfoot = el("div", "foot", "");
    topicsView.appendChild(ttools);
    topicsView.appendChild(cleanBtn);
    topicsView.appendChild(tbox);
    topicsView.appendChild(tfoot);
    tsearch.addEventListener("input", function () { tquery = tsearch.value.trim().toLowerCase(); drawTopics(); });
    treload.addEventListener("click", function () { hap("light"); refresh(); });
    cleanBtn.addEventListener("click", function () {
      var n = (state.topics || []).filter(function (t) { return t.status !== "live"; }).length;
      confirmAsk({ title: "Clean up stopped topics?", message: "Unlink the " + n + " stopped topic" + (n === 1 ? "" : "s") + "? Each topic stays in Telegram as history.", ok: "Clean up", danger: true }, function () {
        hap("light");
        post({ op: "clean_topics" }, topicsView).then(function (r) { toast(r); refresh(); });
      });
    });

    function topicKind(t) { return t.status === "live" ? "live" : "stopped"; }
    function topicMap(t) {
      if (!t.pane) { return "agent stopped"; }
      return "Rex: " + (rexMap(t.session, t.tab, t.paneLabel || t.pane_label) || "pane " + t.pane);
    }
    function topicMatch(t) {
      if (Object.keys(tfilters).length && !tfilters[topicKind(t)]) { return false; }
      if (tquery) {
        var hay = [t.name, t.folder, t.agent, t.session, t.tab, t.pane_label].join(" ").toLowerCase();
        if (hay.indexOf(tquery) < 0) { return false; }
      }
      return true;
    }

    function topicCard(t) {
      var live = t.status === "live";
      var c = el("article", "card " + (live ? "k-working" : "k-idle"));
      var nm = t.name || ("Topic " + t.thread);
      var did = "t" + t.thread;
      var top = el("div", "ctop");
      var rowBtn = el("button", "rowmain");
      rowBtn.type = "button";
      rowBtn.setAttribute("aria-controls", did);
      rowBtn.setAttribute("aria-expanded", "false");
      rowBtn.appendChild(el("span", "cemoji", live ? "🟢" : "⚫"));
      var mid = el("span", "cmid");
      mid.appendChild(el("span", "cname", nm));
      mid.appendChild(el("span", "cstate", topicMap(t)));
      rowBtn.appendChild(mid);
      rowBtn.appendChild(el("span", "pill " + (live ? "live" : "stopped"), live ? "live" : "stopped"));
      rowBtn.appendChild(el("span", "chev", "›"));
      top.appendChild(rowBtn);
      c.appendChild(top);

      var det = el("div", "cdetail");
      det.id = did;
      det.setAttribute("role", "region");
      det.setAttribute("aria-label", "Details of topic " + nm);
      det.hidden = true;
      var where = [t.agent, t.folder].filter(Boolean).join(" · ");
      if (where) { det.appendChild(el("div", "cmeta", where)); }
      det.appendChild(el("div", "cmap", topicMap(t)));
      var tags = el("div", "tags");
      if (t.custom) { tags.appendChild(tag("named by you")); }
      if (t.since) { tags.appendChild(tag("active " + t.since + " ago")); }
      if (tags.children.length) { det.appendChild(tags); }
      var ac = el("div", "cact");
      var op = button(t.pane ? "↗ Open / reconnect" : "↗ Open topic", "btn primary", t.pane ? "Open or reconnect the topic " + nm : "Open the topic " + nm);
      op.addEventListener("click", function () {
        if (t.pane) { openAgentTopic(t.pane, c, refresh); }
        else { hap("light"); openLink(t.link); }
      });
      ac.appendChild(op);
      var rn = button("✏️ Rename", "btn", "Rename " + nm);
      if (!t.pane) { rn.disabled = true; rn.title = "Rename is off: the agent stopped."; }
      rn.addEventListener("click", function () { hap("light"); renameSheet({ pane: t.pane, name: t.name, topic: { name: t.name, custom: t.custom } }); });
      ac.appendChild(rn);
      var ul = button("🔌 Unlink", "btn danger", "Unlink " + nm);
      ul.addEventListener("click", function () {
        hap("light");
        confirmAsk({ title: "Unlink this topic?", message: "rex-tg stops mirroring this session; the topic stays as history.", ok: "Unlink", danger: true }, function () {
          post({ op: "unlink", thread: t.thread }, c).then(function (r) { toast(r); refresh(); });
        });
      });
      ac.appendChild(ul);
      det.appendChild(ac);
      if (!t.pane) { det.appendChild(el("div", "hint", "Rename is off: the agent stopped.")); }
      c.appendChild(det);
      function applyT() {
        var on = !!topen[t.thread];
        rowBtn.setAttribute("aria-expanded", on ? "true" : "false");
        det.hidden = !on;
        c.classList.toggle("open", on);
      }
      rowBtn.addEventListener("click", function () {
        hap("light");
        if (topen[t.thread]) { delete topen[t.thread]; } else { topen[t.thread] = true; }
        applyT();
      });
      applyT();
      return c;
    }

    function drawTopics() {
      var all = state.topics || [];
      var n = { live: 0, stopped: 0 };
      all.forEach(function (t) { n[topicKind(t)]++; });
      Object.keys(tfilters).forEach(function (k) { if (!n[k]) { delete tfilters[k]; } });
      tchips.textContent = "";
      ["live", "stopped"].forEach(function (k) {
        if (!n[k]) { return; }
        var on = !!tfilters[k];
        var b = button("", "sc k-" + (k === "live" ? "working" : "idle") + (on ? " on" : ""));
        b.appendChild(el("i", "dot", ""));
        b.appendChild(document.createTextNode(n[k] + " " + k));
        b.setAttribute("aria-pressed", on ? "true" : "false");
        b.addEventListener("click", function () { hap("light"); if (tfilters[k]) { delete tfilters[k]; } else { tfilters[k] = true; } drawTopics(); });
        tchips.appendChild(b);
      });
      tchips.hidden = !all.length;
      cleanBtn.hidden = !n.stopped;
      cleanBtn.textContent = "🧹 Clean up stopped (" + n.stopped + ")";
      var list = all.filter(topicMatch);
      var seen = {};
      all.forEach(function (t) { seen[t.thread] = true; });
      Object.keys(tcards).forEach(function (k) { if (!seen[k]) { delete tcards[k]; delete topen[k]; } });
      var keep = document.activeElement && tbox.contains(document.activeElement) ? document.activeElement : null;
      tbox.textContent = "";
      if (!all.length) {
        var e = el("div", "empty");
        e.appendChild(el("div", "eico", "💬"));
        e.appendChild(el("div", "etitle", "No topics yet."));
        e.appendChild(el("div", "ehint", "A topic appears when an agent's session is linked to the chat."));
        tbox.appendChild(e);
      } else if (!list.length) {
        var e2 = el("div", "empty");
        e2.appendChild(el("div", "etitle", "No topic matches."));
        var clear = button("Clear filters", "btn");
        clear.addEventListener("click", function () { tfilters = {}; tquery = ""; tsearch.value = ""; drawTopics(); });
        e2.appendChild(clear);
        tbox.appendChild(e2);
      } else {
        list.forEach(function (t) {
          var sig = JSON.stringify(t);
          var cd = tcards[t.thread];
          var busy = cd && cd.el.getAttribute("aria-busy") === "true";
          if (!cd || (cd.sig !== sig && !busy)) { cd = tcards[t.thread] = { sig: sig, el: topicCard(t) }; }
          tbox.appendChild(cd.el);
        });
      }
      if (keep && keep.isConnected) { try { keep.focus({ preventScroll: true }); } catch (e3) { /* gone */ } }
      tfoot.textContent = "Updated " + (state.at || "") + " · " + list.length + " of " + all.length + " shown";
    }

    // --- the New agent tab ---
    function buildNew() {
      var agent = null, folder = null, hidden = false, list = null, loading = 0, fq = "", busy = false, sig = "", loaded = false;
      var inner = el("div", "newinner");
      newView.appendChild(inner);

      inner.appendChild(el("h2", "sect", "Agent"));
      var pickAgent = el("div", "acards");
      pickAgent.setAttribute("role", "radiogroup");
      pickAgent.setAttribute("aria-label", "Agent");
      inner.appendChild(pickAgent);

      inner.appendChild(el("h2", "sect", "Folder"));
      var recent = el("div", "recent");
      var rhead = el("div", "rhead");
      rhead.appendChild(el("span", "rlabel", "Pinned and recent"));
      var cleanF = button("Clean up", "chip", "Remove all recent folders from the list");
      rhead.appendChild(cleanF);
      var rlist = el("div", "rlist");
      rlist.setAttribute("role", "list");
      rlist.setAttribute("aria-label", "Pinned and recent folders");
      var rmore = button("", "chip rmore");
      recent.appendChild(rhead);
      recent.appendChild(rlist);
      recent.appendChild(rmore);
      inner.appendChild(recent);
      var showAllF = false;

      var expl = el("div", "explorer");
      var crumbs = el("nav", "crumbs");
      crumbs.setAttribute("aria-label", "Folder path");
      var etools = el("div", "etools");
      var fin = el("input", "txt");
      fin.type = "search";
      fin.placeholder = "Filter folders here";
      fin.setAttribute("aria-label", "Filter the folders here");
      fin.autocomplete = "off";
      var hid = button("Hidden", "chip", "Show hidden folders");
      hid.setAttribute("role", "switch");
      hid.setAttribute("aria-checked", "false");
      etools.appendChild(fin);
      etools.appendChild(hid);
      var rowsBox = el("div", "frows");
      rowsBox.setAttribute("aria-live", "polite");
      var useBtn = button("Use this folder", "btn wide");
      expl.appendChild(crumbs);
      expl.appendChild(etools);
      expl.appendChild(useBtn);
      expl.appendChild(rowsBox);
      inner.appendChild(expl);

      var summary = el("div", "summary");
      var why = el("div", "hint");
      var startBar = el("div", "startbar");
      var startBtn = button("", "btn primary");
      startBar.appendChild(startBtn);
      inner.appendChild(summary);
      inner.appendChild(why);
      newView.appendChild(startBar); // hidden with the tab

      function starters() { return (state && state.starters) || []; }
      function chosenStarter() { return starters().filter(function (x) { return x.key === agent; })[0]; }

      function updateStart() {
        var s = chosenStarter();
        var paused = !!(state && state.paused);
        var ready = !!(s && folder && !paused && !busy);
        var text = busy ? "Starting…" : (s && folder ? "Start " + s.name + " in " + folder.shown : "Start an agent");
        summary.textContent = "";
        if (s && folder) {
          summary.appendChild(document.createTextNode("Start "));
          summary.appendChild(el("b", "", s.name));
          summary.appendChild(document.createTextNode(" in "));
          summary.appendChild(el("code", "", folder.shown));
        } else {
          summary.appendChild(el("span", "muted", "Pick an agent and a folder."));
        }
        why.textContent = paused ? "⏸ Typing is paused, so a new agent cannot start. Send /unpause in the chat." : (!s ? "Pick an agent." : (!folder ? "Pick a folder." : ""));
        startBtn.textContent = text;
        startBtn.disabled = !ready;
      }

      function start() {
        var s = chosenStarter();
        if (!s || !folder || busy || (state && state.paused)) { return; }
        busy = true;
        updateStart();
        hap("light");
        post({ op: "new", agent: s.key, path: folder.path }, newView).then(function (r) {
          busy = false;
          toast(r, "Progress continues in the main chat.");
          afterStart(r, s.name + " in " + folder.shown);
          updateStart();
          refresh();
        });
      }
      startBtn.addEventListener("click", start);

      function drawAgentPick() {
        pickAgent.textContent = "";
        starters().forEach(function (s) {
          var on = s.key === agent;
          var b = button("", "acard" + (on ? " on" : ""));
          b.setAttribute("role", "radio");
          b.setAttribute("aria-checked", on ? "true" : "false");
          b.appendChild(el("span", "an", s.name));
          b.addEventListener("click", function () { hap("light"); agent = s.key; drawAgentPick(); updateStart(); });
          pickAgent.appendChild(b);
        });
        if (!starters().length) { pickAgent.appendChild(el("div", "muted", "No agents can be started.")); }
      }
      function pick(f) {
        folder = { path: f.path, shown: f.shown };
        hap("light");
        drawChips();
        drawUse();
        updateStart();
      }
      function splitPath(sh) {
        var i = sh.lastIndexOf("/");
        return i < 0 || i === sh.length - 1 ? ["", sh] : [sh.slice(0, i + 1), sh.slice(i + 1)];
      }
      function drawChips() {
        rlist.textContent = "";
        var fs = (state && state.folders) || [];
        var limit = 6, cut = !showAllF && fs.length > limit;
        (cut ? fs.slice(0, limit) : fs).forEach(function (f) {
          var on = !!(folder && folder.path === f.path);
          var row = el("div", "fitem" + (on ? " on" : ""));
          row.setAttribute("role", "listitem");
          var m = button("", "fmain", (f.pinned ? "Pinned folder " : "Recent folder ") + f.shown);
          m.setAttribute("aria-pressed", on ? "true" : "false");
          m.appendChild(el("span", "ic", f.pinned ? "📌" : "📁"));
          var sp = splitPath(f.shown), p = el("span", "fpath");
          if (sp[0]) { p.appendChild(el("span", "fhead", sp[0])); }
          p.appendChild(el("span", "ftail", sp[1]));
          m.appendChild(p);
          m.addEventListener("click", function () { pick(f); load(f.path); });
          row.appendChild(m);
          if (!f.pinned) {
            var x = button("✕", "fx", "Remove " + f.shown + " from recent folders");
            x.addEventListener("click", function () {
              hap("light");
              post({ op: "forget_folder", path: f.path }, row).then(function (r) { toast(r); refresh(); });
            });
            row.appendChild(x);
          }
          rlist.appendChild(row);
        });
        if (!fs.length) { rlist.appendChild(el("div", "tnote", "No recent folders yet. Pick one below.")); }
        rmore.hidden = fs.length <= 6;
        rmore.textContent = showAllF ? "Show fewer" : "Show all (" + fs.length + ")";
        rmore.setAttribute("aria-expanded", showAllF ? "true" : "false");
        cleanF.hidden = !fs.some(function (f) { return !f.pinned; });
      }
      rmore.addEventListener("click", function () { showAllF = !showAllF; drawChips(); });
      cleanF.addEventListener("click", function () {
        hap("light");
        confirmAsk({ title: "Clean up recent folders?", message: "Remove all recent folders from the list? Pinned folders stay. A folder comes back when an agent runs there again.", ok: "Remove", danger: true }, function () {
          post({ op: "forget_folders" }, recent).then(function (r) { toast(r); refresh(); });
        });
      });
      function drawUse() {
        var cur = list;
        useBtn.hidden = !cur;
        if (!cur) { return; }
        var sel = !!(folder && folder.path === cur.path);
        useBtn.textContent = sel ? "✓ This folder is selected" : "Use this folder";
        useBtn.className = sel ? "btn wide" : "btn primary wide";
        useBtn.setAttribute("aria-label", (sel ? "Selected: " : "Use folder ") + cur.shown);
      }

      function gitBadge() { var g = el("span", "gitb", "git"); g.setAttribute("aria-label", "git repository"); return g; }

      function drawCrumbs(l) {
        crumbs.textContent = "";
        var home = (state && state.home && state.home.path) || "";
        var segs = [{ shown: (state && state.home && state.home.shown) || "~", path: home }];
        if (l.path !== home) {
          if (home && l.path.indexOf(home + "/") === 0) {
            var p = home;
            l.path.slice(home.length + 1).split("/").forEach(function (cpart) { p += "/" + cpart; segs.push({ shown: cpart, path: p }); });
          } else { segs = [{ shown: l.shown, path: l.path }]; }
        }
        segs.forEach(function (sg, i) {
          if (i) { var sp = el("span", "sep", "›"); sp.setAttribute("aria-hidden", "true"); crumbs.appendChild(sp); }
          var last = i === segs.length - 1;
          var b = button(sg.shown, "crumb" + (last ? " last" : ""), "Go to " + sg.shown);
          if (last) { b.setAttribute("aria-current", "location"); }
          b.addEventListener("click", function () { if (!last) { hap("light"); load(sg.path); } });
          crumbs.appendChild(b);
        });
        if (l.git) { crumbs.appendChild(gitBadge()); }
        crumbs.scrollLeft = crumbs.scrollWidth;
      }

      // drawRows shows the folder l, or err: why the folder at path did
      // not open, with a way to try it again and a way back to l.
      function drawRows(l, err, path) {
        rowsBox.textContent = "";
        if (err) {
          rowsBox.appendChild(el("div", "etitle", err));
          var rt = button("Try again", "btn small");
          rt.addEventListener("click", function () { hap("light"); load(path); });
          rowsBox.appendChild(rt);
          if (l) {
            var back = button("Back to " + l.shown, "btn small");
            back.addEventListener("click", function () { hap("light"); drawRows(l); });
            rowsBox.appendChild(back);
          }
          return;
        }
        if (l.parent) {
          var up = button("↑ Up", "frow up", "Up to the folder above");
          up.addEventListener("click", function () { hap("light"); load(l.parent); });
          rowsBox.appendChild(up);
        }
        var q = fq.toLowerCase();
        var dirs = l.dirs.filter(function (d) { return !q || d.shown.toLowerCase().indexOf(q) >= 0; });
        dirs.forEach(function (d) {
          var r = button("", "frow", "Open folder " + d.shown + (d.git ? ", git repository" : ""));
          r.appendChild(el("span", "ic", "📁"));
          r.appendChild(el("span", "nm", d.shown));
          if (d.git) { r.appendChild(gitBadge()); }
          r.appendChild(el("span", "chev", "›"));
          r.addEventListener("click", function () { hap("light"); load(d.path); });
          rowsBox.appendChild(r);
        });
        if (!dirs.length) { rowsBox.appendChild(el("div", "tnote", fq ? "No folder here matches “" + fq + "”." : "No folders here.")); }
        if (l.more) { rowsBox.appendChild(el("div", "tnote", "… more folders not shown")); }
      }

      // shownPath shows a path inside home with "~", as the server does.
      function shownPath(p) {
        var home = (state && state.home && state.home.path) || "";
        if (!p || p === home) { return "~"; }
        return home && p.indexOf(home + "/") === 0 ? "~" + p.slice(home.length) : p;
      }

      function load(path, keepFilter) {
        var mine = ++loading;
        path = path || (state && state.home && state.home.path) || "";
        if (!keepFilter) { fq = ""; fin.value = ""; }
        expl.setAttribute("aria-busy", "true");
        rowsBox.textContent = "";
        // The crumbs still show the folder before; this line names the one
        // that opens.
        rowsBox.appendChild(el("div", "tnote", "Opening " + shownPath(path) + "…"));
        for (var i = 0; i < 4; i++) { rowsBox.appendChild(el("div", "skel row-skel")); }
        // The Mac answers within 8 seconds (it says when macOS keeps a
        // folder closed); a longer wait is the connection.
        api("api/control/folders?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(path) + (hidden ? "&hidden=1" : ""), undefined, 20000)
          .then(function (l) {
            if (mine !== loading) { return; }
            list = l;
            drawCrumbs(l);
            drawRows(l);
            drawUse();
          })
          .catch(function (e) {
            if (mine !== loading) { return; }
            drawRows(list, e.message || "This folder could not be loaded.", path);
          })
          .then(function () {
            if (mine !== loading) { return; }
            expl.removeAttribute("aria-busy");
            syncBack();
          });
      }

      fin.addEventListener("input", function () { fq = fin.value.trim(); if (list) { drawRows(list); } });
      hid.addEventListener("click", function () {
        hap("light");
        hidden = !hidden;
        hid.className = "chip" + (hidden ? " on" : "");
        hid.setAttribute("aria-checked", hidden ? "true" : "false");
        load(list ? list.path : state.home.path, true);
      });
      useBtn.addEventListener("click", function () { if (list) { pick(list); } });

      return {
        canUp: function () { return !!(list && list.parent); },
        up: function () { if (list && list.parent) { load(list.parent); } },
        shown: function () {
          if (!loaded && state) { loaded = true; load(state.home.path); }
          updateStart();
        },
        // update takes a new state: it redraws only what changed, so the form stays as it is
        update: function () {
          var s = JSON.stringify([state.starters, state.folders]);
          if (!agent || !starters().some(function (x) { return x.key === agent; })) { agent = starters().length ? starters()[0].key : null; }
          if (s !== sig) { sig = s; drawAgentPick(); drawChips(); }
          if (tab === "new" && !loaded) { loaded = true; load(state.home.path); }
          updateStart();
        }
      };
    }

    function render() {
      matchStarts();
      drawHead();
      drawAgents();
      drawTopics();
      if (!newPanel) { newPanel = buildNew(); setTab(tab); }
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
          errBar.hidden = true;
          render();
        })
        .catch(function (e) {
          if (mine !== asked) { return; }
          errBar.textContent = (e.message || "Could not read the agents.") + " Trying again soon.";
          errBar.hidden = false;
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
    setTab(tab);
    refresh();
  }

  // --- the keyboard's buttons: launch ---

  var viewName = { panel: "Panel", screen: "Screen", files: "Files", changes: "Changes", annotate: "Annotate" };

  function skeletons(n, cls) {
    page.className = "";
    page.textContent = "";
    for (var i = 0; i < n; i++) { page.appendChild(el("div", "skel " + (cls || "card-skel"))); }
    page.style.display = "flex";
    page.style.flexDirection = "column";
    page.style.gap = "8px";
  }

  // launch opens the page of a keyboard button: the server makes (or finds)
  // the page, or asks which topic.
  function launch(go, thread) {
    document.body.classList.remove("fill");
    setBack(null);
    setTitle(viewName[go] || "rex-tg");
    skeletons(3);
    api("api/launch?go=" + encodeURIComponent(go) + (thread ? "&t=" + encodeURIComponent(thread) : ""))
      .then(function (r) {
        if (r.page) { location.replace(appURL({ p: r.page, from: "kb" })); return; }
        page.removeAttribute("style");
        page.className = "";
        page.textContent = "";
        if (r.pick) { picker(go, r.pick); return; }
        launchError(r.error || "This page could not be opened.");
      })
      .catch(function (e) {
        page.removeAttribute("style");
        launchError(e.message || "This page could not be opened.");
      });
  }

  function launchError(msg) {
    page.className = "";
    page.textContent = "";
    var box = el("div", "empty");
    box.appendChild(el("div", "etitle", msg));
    var back = button("Back to the panel", "btn primary");
    back.addEventListener("click", function () { location.href = appURL({ go: "panel" }); });
    box.appendChild(back);
    page.appendChild(box);
  }

  function picker(go, topics) {
    setTitle("Pick a topic");
    var list = topics.slice().sort(function (a, b) { return (b.live ? 1 : 0) - (a.live ? 1 : 0); });
    page.className = "";
    page.textContent = "";
    page.appendChild(el("p", "shint", (viewName[go] || "This page") + " of which topic?"));
    if (!list.length) {
      var e = el("div", "empty");
      e.appendChild(el("div", "etitle", "No topics yet."));
      e.appendChild(el("div", "ehint", "A topic appears when an agent's session is linked to the chat."));
      page.appendChild(e);
      return;
    }
    var box = el("div", "pick");
    list.forEach(function (t) {
      var b = button("", "prow", (t.live ? "Live topic " : "Topic ") + t.name);
      var dot = el("span", "pdot" + (t.live ? " live" : ""));
      dot.setAttribute("aria-hidden", "true");
      b.appendChild(dot);
      b.appendChild(el("span", "pname", t.name));
      b.appendChild(el("span", "chev", "›"));
      b.addEventListener("click", function () { hap("light"); launch(go, t.thread); });
      box.appendChild(b);
    });
    page.appendChild(box);
  }

  // --- the Annotate page ---

  var annotModes = ["send", "copy", "box"];
  var annotModeLabel = { send: "Send to agent", copy: "Copy", box: "Put in message box (beta)" };
  var annotModeButton = { send: "Send to agent", copy: "Copy notes", box: "Put in message box" };

  // sheetUI shows a bottom sheet. build(box, close) fills it; Back closes it.
  function sheetUI(label, build, onClose) {
    var prev = document.activeElement;
    var scrim = el("div", "scrim");
    var box = el("div", "sheet");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-modal", "true");
    box.setAttribute("aria-label", label);
    box.appendChild(el("div", "grip"));
    box.appendChild(el("h3", "stitle", label));
    scrim.appendChild(box);
    var vv = window.visualViewport;
    function fit() {
      if (!vv) { return; }
      scrim.style.height = vv.height + "px";
      scrim.style.top = vv.offsetTop + "px";
      scrim.style.bottom = "auto";
      box.style.maxHeight = Math.floor(vv.height * 0.9) + "px";
    }
    function onKey(e) { if (e.key === "Escape") { close(); } }
    var closed = false;
    function close(quiet) {
      if (closed) { return; }
      closed = true;
      if (scrim.parentNode) { scrim.parentNode.removeChild(scrim); }
      document.removeEventListener("keydown", onKey);
      if (vv) { vv.removeEventListener("resize", fit); vv.removeEventListener("scroll", fit); }
      document.body.style.overflow = "";
      setBack(null);
      if (quiet !== true && prev && prev.focus) { try { prev.focus({ preventScroll: true }); } catch (e) { /* gone */ } }
      if (quiet !== true && onClose) { onClose(); }
    }
    scrim.addEventListener("click", function (e) { if (e.target === scrim) { close(); } });
    document.addEventListener("keydown", onKey);
    if (vv) { vv.addEventListener("resize", fit); vv.addEventListener("scroll", fit); fit(); }
    build(box, close);
    document.body.appendChild(scrim);
    document.body.style.overflow = "hidden";
    setBack(close);
    return close;
  }

  // annotatePage shows the Annotate page (the answers of a topic, loaded
  // here); with whole set, it makes the one answer already in page
  // annotatable (the "Whole answer" page). pg is the page's own data.
  function annotatePage(pg, whole) {
    document.body.classList.remove("fill");
    document.body.classList.add("annot");
    if (!whole) {
      page.className = "annot-page";
      page.textContent = "";
    }

    // --- the saved notes of this page (kept on this phone) ---
    var storeKey = "rex-tg.annotate." + id;
    var notes = [], msg = "";
    // maybeAt: when a send may have arrived although the page stayed open
    // (asks first for an hour). pend: a signed send whose result is not
    // known ({act, text, at}; its act is kept for 10 minutes).
    var maybeAt = 0, pend = null;
    var hour = 3600 * 1000, pendTTL = 10 * 60 * 1000;
    try {
      var saved = JSON.parse(load(storeKey) || "null");
      if (saved && saved.notes instanceof Array) { notes = saved.notes.filter(function (n) { return n && n.id && n.note; }); }
      if (saved && typeof saved.msg === "string") { msg = saved.msg; }
      if (saved && typeof saved.maybe === "number" && Date.now() - saved.maybe < hour) { maybeAt = saved.maybe; }
      if (saved && saved.pend && typeof saved.pend.act === "string" && typeof saved.pend.text === "string" && Date.now() - saved.pend.at < pendTTL) { pend = saved.pend; }
    } catch (e) { /* no usable draft */ }
    function persist() {
      var keep = notes.length || msg || maybeAt || pend;
      save(storeKey, keep ? JSON.stringify({ notes: notes, msg: msg, maybe: maybeAt || undefined, pend: pend || undefined }) : null);
    }
    function isMaybe() { return !!maybeAt && Date.now() - maybeAt < hour; }
    // The notes that were sent are kept for an hour, to bring back.
    var sentKey = storeKey + ".sent";
    function keepSent(ns, m, maybe) {
      save(sentKey, JSON.stringify({ notes: ns, msg: m, at: Date.now(), maybe: !!maybe }));
      backup = readSent();
    }
    function dropSent() { save(sentKey, null); backup = null; }
    function readSent() {
      try {
        var b = JSON.parse(load(sentKey) || "null");
        if (b && b.notes instanceof Array && b.notes.length && Date.now() - b.at < hour) { return b; }
      } catch (e) { /* none */ }
      save(sentKey, null);
      return null;
    }
    var backup = readSent();
    var mode = load("rex-tg.annotate.mode");
    if (annotModes.indexOf(mode) < 0) { mode = "send"; }
    var bot = (pg && pg.bot) || "";

    function order(a, b) {
      return (a.item - b.item) || (a.ord - b.ord) || ((a.ts || 0) - (b.ts || 0));
    }
    function sorted() { return notes.slice().sort(order); }
    function find(nid) {
      for (var i = 0; i < notes.length; i++) { if (notes[i].id === nid) { return notes[i]; } }
      return null;
    }

    // --- the text to send ---
    // cut keeps 500 characters (code points: a pair is never split).
    function cut(q) {
      q = q.replace(/\s+$/, "");
      if (q.length <= 500) { return q; }
      var cps = Array.from ? Array.from(q) : q.split("");
      return cps.length > 500 ? cps.slice(0, 500).join("") + "…" : q;
    }
    // scrub turns a half of a surrogate pair into U+FFFD, so the text can be encoded.
    function scrub(t) {
      var out = "";
      for (var i = 0; i < t.length; i++) {
        var c = t.charCodeAt(i);
        if (c >= 0xd800 && c <= 0xdbff) {
          var d = t.charCodeAt(i + 1);
          if (d >= 0xdc00 && d <= 0xdfff) { out += t.charAt(i) + t.charAt(i + 1); i++; } else { out += "\ufffd"; }
        } else if (c >= 0xdc00 && c <= 0xdfff) { out += "\ufffd"; } else { out += t.charAt(i); }
      }
      return out;
    }
    function compose() {
      var parts = [];
      if (msg.trim()) { parts.push(msg.trim()); }
      var cur = null;
      sorted().forEach(function (n) {
        if (cur !== n.item) {
          cur = n.item;
          parts.push(n.at ? "Notes on your answer from " + n.at + ":" : "Notes on your answer:");
        }
        parts.push(cut(n.quote).split("\n").map(function (l) { return "> " + l; }).join("\n"));
        parts.push(n.note.trim());
      });
      return scrub(parts.join("\n\n"));
    }

    // --- the layout ---
    var list = el("div", "alist");
    var foot = el("div", "afoot");
    var sentinel = el("div", "asent");
    if (!whole) {
      page.appendChild(list);
      page.appendChild(foot);
      page.appendChild(sentinel);
    }

    var bar = el("div", "abar");
    var count = el("span", "acount", "");
    count.setAttribute("aria-live", "polite");
    var review = button("Review", "btn primary");
    bar.appendChild(count);
    bar.appendChild(review);
    document.body.appendChild(bar);
    var pill = button("✍️ Note on selection", "selpill");
    pill.hidden = true;
    document.body.appendChild(pill);

    var restoreRow = el("div", "abackup");
    var restoreText = el("span", "", "");
    var restoreBtn = button("Bring back", "btn small");
    restoreRow.appendChild(restoreText);
    restoreRow.appendChild(restoreBtn);
    restoreRow.hidden = true;
    document.body.appendChild(restoreRow);
    restoreBtn.addEventListener("click", function () {
      if (!backup) { return; }
      hap("light");
      notes = backup.notes.filter(function (n) { return n && n.id && n.note; });
      if (!msg) { msg = backup.msg || ""; }
      if (backup.maybe) { maybeAt = Date.now(); }
      dropSent();
      persist();
      refreshBadges();
    });

    var blocks = {};   // by note id: the elements of the loaded answers
    var items = {};    // by answer id: { at }
    var oldest = 0, more = true, loading = false, failed = "", firstLoad = true;

    function syncBar() {
      var n = notes.length;
      count.textContent = n + (n === 1 ? " note" : " notes");
      review.disabled = false;
      if (backup && Date.now() - backup.at >= hour) { dropSent(); }
      restoreRow.hidden = !(backup && !n);
      if (backup) {
        var d = new Date(backup.at);
        restoreText.textContent = "Notes sent at " + (d.getHours() < 10 ? "0" : "") + d.getHours() + ":" + (d.getMinutes() < 10 ? "0" : "") + d.getMinutes() + " · ";
      }
    }

    // A table row cannot hold a badge: its first cell does.
    function badgeHost(b) { return b.tagName === "TR" && b.firstElementChild ? b.firstElementChild : b; }

    // refreshBadges marks the blocks that have a note, and numbers them as Review does.
    function refreshBadges() {
      Object.keys(blocks).forEach(function (k) {
        blocks[k].forEach(function (b) {
          b.classList.remove("noted");
          var host = badgeHost(b);
          var old = host.querySelector(".nbadge");
          if (old && old.parentNode === host) { host.removeChild(old); }
        });
      });
      sorted().forEach(function (n, i) {
        (blocks[n.id] || []).forEach(function (b) {
          b.classList.add("noted");
          badgeHost(b).appendChild(el("span", "nbadge", String(i + 1)));
        });
      });
      syncBar();
    }

    function setNote(n) {
      var old = find(n.id);
      if (old) { notes[notes.indexOf(old)] = n; } else { notes.push(n); }
      persist();
      refreshBadges();
    }
    function dropNote(nid) {
      notes = notes.filter(function (n) { return n.id !== nid; });
      persist();
      refreshBadges();
    }

    // --- the note sheet ---
    // o: { id, item, at, ord, quote }; again runs after the sheet closes.
    function noteSheet(o, again) {
      var existing = find(o.id);
      hap("light");
      sheetUI(existing ? "Edit note" : "Add a note", function (box, close) {
        var q = el("button", "aquote", o.quote);
        q.type = "button";
        q.setAttribute("aria-expanded", "false");
        q.setAttribute("aria-label", "Quoted text. Tap to show all of it.");
        q.addEventListener("click", function () {
          var on = q.classList.toggle("open");
          q.setAttribute("aria-expanded", on ? "true" : "false");
        });
        box.appendChild(q);
        var lab = el("label", "alabel", "Your note");
        var ta = el("textarea", "atxt");
        ta.id = "note-text";
        lab.setAttribute("for", "note-text");
        ta.rows = 4;
        ta.value = existing ? existing.note : "";
        ta.setAttribute("autocapitalize", "sentences");
        box.appendChild(lab);
        box.appendChild(ta);
        var row = el("div", "srow");
        var sv = button("Save", "btn primary big");
        var del = button("Delete", "btn danger big");
        var cancel = button("Cancel", "btn big");
        row.appendChild(sv);
        if (existing) { row.appendChild(del); }
        row.appendChild(cancel);
        box.appendChild(row);
        function ok() { sv.disabled = !ta.value.trim(); }
        ok();
        ta.addEventListener("input", ok);
        sv.addEventListener("click", function () {
          if (!ta.value.trim()) { return; }
          setNote({ id: o.id, item: o.item, at: o.at, ord: o.ord, quote: o.quote, note: ta.value.trim(), ts: existing ? existing.ts : Date.now() });
          hap("success");
          close();
        });
        del.addEventListener("click", function () { dropNote(o.id); hap("light"); close(); });
        cancel.addEventListener("click", function () { close(); });
        setTimeout(function () { try { ta.focus(); } catch (e) { /* none */ } }, 30);
      }, again);
    }

    // --- the review sheet ---
    var reviewClose = null;
    // maybeSent: a send whose page Telegram did not close; the next send
    // asks first, so the notes do not reach the agent twice by mistake.
    
    function reviewSheet() {
      hap("light");
      sheetUI("Review", function (box, close) {
        reviewClose = close;
        var listBox = el("div", "rlistn");
        function draw() {
          listBox.textContent = "";
          var all = sorted();
          if (!all.length) { listBox.appendChild(el("p", "shint", "No notes yet. Tap a paragraph in an answer to add one.")); }
          all.forEach(function (n, i) {
            var row = el("div", "rnote");
            var main = el("button", "rmainn");
            main.type = "button";
            main.setAttribute("aria-label", "Edit note " + (i + 1));
            main.appendChild(el("span", "nbadge flat", String(i + 1)));
            var t = el("span", "rtext");
            t.appendChild(el("span", "rquote", n.quote));
            t.appendChild(el("span", "rbody", n.note));
            t.appendChild(el("span", "rat", n.at));
            main.appendChild(t);
            main.addEventListener("click", function () {
              close(true);
              noteSheet({ id: n.id, item: n.item, at: n.at, ord: n.ord, quote: n.quote }, reviewSheet);
            });
            var x = button("✕", "fx", "Delete note " + (i + 1));
            x.addEventListener("click", function () {
              hap("light");
              confirmAsk({ title: "Delete this note?", message: "The note is removed from the list.", ok: "Delete", danger: true }, function () {
                dropNote(n.id);
                draw();
                sync();
              });
            });
            row.appendChild(main);
            row.appendChild(x);
            listBox.appendChild(row);
          });
        }
        box.appendChild(listBox);

        var mlab = el("label", "alabel", "Message to the agent (optional)");
        mlab.setAttribute("for", "note-msg");
        var mta = el("textarea", "atxt");
        mta.id = "note-msg";
        mta.rows = 3;
        mta.value = msg;
        box.appendChild(mlab);
        box.appendChild(mta);

        var seg2 = el("div", "segc stack");
        seg2.setAttribute("role", "radiogroup");
        seg2.setAttribute("aria-label", "How to send");
        box.appendChild(seg2);
        var hint = el("p", "shint", "");
        box.appendChild(hint);
        var go = button("", "btn primary big wide");
        box.appendChild(go);
        var busy = false;

        function canSend() { return !!(sorted().length || mta.value.trim()); }
        function sync() {
          go.textContent = busy ? "Sending…" : annotModeButton[mode];
          go.disabled = busy || !canSend();
          Array.prototype.forEach.call(seg2.children, function (b) {
            var on = b.getAttribute("data-mode") === mode;
            b.className = on ? "on" : "";
            b.setAttribute("aria-checked", on ? "true" : "false");
          });
          hint.textContent = mode === "send" && !sendPath() ? noSendText() : "";
        }
        annotModes.forEach(function (m) {
          var b = el("button", "", annotModeLabel[m]);
          b.type = "button";
          b.setAttribute("role", "radio");
          b.setAttribute("data-mode", m);
          b.addEventListener("click", function () { hap("light"); mode = m; save("rex-tg.annotate.mode", m === "send" ? null : m); sync(); });
          seg2.appendChild(b);
        });
        mta.addEventListener("input", function () { msg = mta.value; persist(); sync(); });

        go.addEventListener("click", function () {
          msg = mta.value;
          persist();
          var text = compose();
          if (!text) { return; }
          if (mode === "copy") { copyText(text); return; }
          if (mode === "box") { putInBox(text); return; }
          if (!sendPath()) {
            toast({ ok: false, message: noSendText() });
            mode = "copy";
            sync();
            return;
          }
          if (utf8Length(text) > 16000) {
            toast({ ok: false, message: "These notes are too long to send (16000 bytes at most). Remove a note, or use Copy." });
            return;
          }
          if (isMaybe() && sendPath() === "draft") {
            // An earlier send may have arrived although the page stayed open.
            confirmAsk({ title: "Send again?", message: "These notes may have reached the agent already. Send them again only if \u201c\u270d\ufe0f Notes sent to the agent\u201d is not in the topic.", ok: "Send again" }, function () {
              maybeAt = 0;
              persist();
              sendNow(text);
            });
            return;
          }
          sendNow(text);
        });
        // sendSigned sends through the server (signed init data). A result that
        // is not known yet is asked again with the same act, so it never runs twice.
        function sendSigned(text) {
          var live = pend && Date.now() - pend.at < pendTTL ? pend : null;
          if (live && live.text !== text) {
            confirmAsk({ title: "Send these notes too?", message: "The earlier send may still reach the agent. Send these notes too?", ok: "Send" }, function () {
              pend = null;
              run(text);
            });
            return;
          }
          run(text);
        }
        function run(text) {
          var live = pend && Date.now() - pend.at < pendTTL && pend.text === text ? pend : null;
          var mine = live ? live.act : act(), tries = 0;
          pend = { act: mine, text: text, at: live ? live.at : Date.now() };
          // what this send holds: later notes stay when it succeeds
          var ids = sorted().map(function (n) { return n.id; });
          var sentNotes = sorted(), sentMsg = msg;
          persist();
          busy = true;
          sync();
          function finish(r) {
            busy = false;
            if (r.pending) {
              toast(r);
            } else {
              pend = null;
              if (r.ok) {
                keepSent(sentNotes, sentMsg, false);
                notes = notes.filter(function (n) { return ids.indexOf(n.id) < 0; });
                if (msg === sentMsg) { msg = ""; }
                persist();
                refreshBadges();
                toast(r);
                close();
                return;
              }
              persist();
            }
            if (!r.pending) { toast(r); }
            sync();
          }
          function attempt() {
            api("api/annotate/send?id=" + encodeURIComponent(id), { act: mine, text: text })
              .then(function (r) {
                if (r.pending && tries < 4) { tries++; setTimeout(attempt, 1500); return; }
                finish(r);
              })
              .catch(function (e) {
                var m = e.message || "The notes were not sent.";
                if (!e.status || e.status >= 500) {
                  if (tries < 4) { tries++; setTimeout(attempt, 1500); return; }
                  finish({ ok: false, pending: true, message: m + " The notes may still arrive." });
                  return;
                }
                finish({ ok: false, message: m });
              });
          }
          attempt();
        }
        function sendNow(text) {
          if (sendPath() === "signed") { sendSigned(text); return; }
          busy = true;
          sync();
          api("api/annotate/draft?id=" + encodeURIComponent(id), { text: text })
            .then(function (r) {
              if (!r.draft) { throw new Error("The draft was not saved."); }
              var held = { notes: notes, msg: msg };
              keepSent(notes, msg, true);
              notes = [];
              msg = "";
              maybeAt = 0;
              persist();
              refreshBadges();
              function restore() {
                notes = held.notes;
                msg = held.msg;
                dropSent();
                persist();
                refreshBadges();
              }
              try {
                tg.sendData(JSON.stringify({ v: 1, a: "annotate", d: r.draft }));
              } catch (e) {
                restore();
                throw new Error("Telegram did not take the notes. They are kept here.");
              }
              hap("success");
              // Telegram closes the page when it takes the data. A page
              // that is still open after a while was not closed: the notes
              // come back, and the user checks the topic before sending
              // again (the draft may have arrived).
              setTimeout(function () {
                maybeAt = Date.now();
                restore();
                busy = false;
                sync();
                toast({ ok: false, message: "Telegram did not close this page. Check the topic: if “✍️ Notes sent to the agent” is not there, send again." });
              }, 6000);
            })
            .catch(function (e) {
              busy = false;
              toast({ ok: false, message: e.message || "The notes were not sent." });
              sync();
            });
        }
        draw();
        sync();
      }, function () { reviewClose = null; });
    }
    review.addEventListener("click", reviewSheet);

    // sendPath: how "Send to agent" can work here. "draft": the keyboard's
    // page sends through Telegram. "signed": a page opened from a message
    // asks the server. "": no way.
    function sendPath() {
      if (tg && tg.sendData && keyMode) { return "draft"; }
      if (!keyMode && initData && pg && pg.thread) { return "signed"; }
      return "";
    }
    function noSendText() {
      if (pg && pg.kind === "answer" && !pg.thread) { return "This answer is from before Annotate; use Copy."; }
      return "Send to agent works when Annotate is opened from its button under the message box. Use Copy instead.";
    }
    function utf8Length(s) {
      try { return unescape(encodeURIComponent(s)).length; } catch (e) { return s.length * 3; }
    }

    function copyText(text) {
      function done() { toast({ ok: true, message: "Copied. Paste it in the topic." }); }
      // The hidden textarea copies inside the tap (iOS needs that); the
      // clipboard API is the second try.
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.readOnly = true;
      ta.setAttribute("aria-hidden", "true");
      ta.style.cssText = "position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;";
      document.body.appendChild(ta);
      ta.select();
      try { ta.setSelectionRange(0, text.length); } catch (e) { /* older browser */ }
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      if (ok) { done(); return; }
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () { byHand(text); });
      } else {
        byHand(text);
      }
    }
    function byHand(text) {
      toast({ ok: false, message: "Could not copy. Select the text and copy it." });
      sheetUI("Copy by hand", function (box, close) {
        box.appendChild(el("p", "shint", "Select the text and copy it."));
        var ta = el("textarea", "atxt");
        ta.readOnly = true;
        ta.rows = 10;
        ta.value = text;
        box.appendChild(ta);
        var row = el("div", "srow");
        var ok = button("Close", "btn primary big");
        ok.addEventListener("click", function () { close(); });
        row.appendChild(ok);
        box.appendChild(row);
        setTimeout(function () { try { ta.focus(); ta.select(); } catch (e) { /* none */ } }, 30);
      });
    }

    function putInBox(text) {
      if (!bot) { toast({ ok: false, message: "The bot's name is not known. Use Copy." }); return; }
      if (!tg || !tg.openTelegramLink) { toast({ ok: false, message: "This Telegram cannot do that. Use Copy." }); return; }
      if (text.length > 3500) { toast({ ok: false, message: "These notes are too long for the message box. Use Copy or Send to agent." }); return; }
      var link;
      try { link = "https://t.me/" + bot + "?text=" + encodeURIComponent(text); } catch (e) {
        toast({ ok: false, message: "These notes could not be put in the message box. Use Copy." });
        return;
      }
      tg.openTelegramLink(link);
    }

    // --- the answers ---
    // ownText is the text of an element without its nested blocks.
    var nested = { UL: 1, OL: 1, P: 1, BLOCKQUOTE: 1, PRE: 1, TABLE: 1, DIV: 1 };
    function ownText(e) {
      var s = "";
      for (var n = e.firstChild; n; n = n.nextSibling) {
        if (n.nodeType === 1 && nested[n.tagName]) { continue; }
        if (n.nodeType === 1 && n.classList.contains("nbadge")) { continue; }
        s += n.textContent;
      }
      return s;
    }
    function oneLine(s) { return s.replace(/\s+/g, " ").trim(); }

    // markBlocks makes the paragraphs, items, headings, quotes, table rows,
    // and code lines of one answer tappable.
    function markBlocks(body, it) {
      var pres = body.querySelectorAll("pre");
      for (var i = 0; i < pres.length; i++) {
        var code = pres[i].querySelector("code") || pres[i];
        if (code.children.length) { continue; } // text with markup: the whole block stays one
        var lines = code.textContent.replace(/\n$/, "").split("\n");
        code.textContent = "";
        lines.forEach(function (l) {
          var sp = el("span", "cl", l);
          code.appendChild(sp);
        });
      }
      var cand = body.querySelectorAll("p, li, h1, h2, h3, h4, h5, h6, blockquote, tr, .cl, pre");
      var ord = 0;
      for (var j = 0; j < cand.length; j++) {
        var e = cand[j], tag = e.tagName, q;
        if (tag === "PRE") { if (e.querySelector(".cl")) { continue; } q = e.textContent.replace(/\s+$/, ""); }
        else if (tag === "TR") {
          q = Array.prototype.map.call(e.children, function (c) { return oneLine(c.textContent); }).join(" | ");
        }
        else if (tag === "LI" || tag === "BLOCKQUOTE") { q = oneLine(ownText(e)); }
        else if (e.classList.contains("cl")) { q = e.textContent.replace(/\s+$/, ""); if (!q.trim()) { continue; } }
        else { q = oneLine(e.textContent); }
        if (!q.trim()) { continue; }
        var nid = it.id + ":" + ord;
        e.setAttribute("data-bk", nid);
        e.setAttribute("tabindex", "0");
        e._bk = { id: nid, item: it.id, at: it.at, ord: ord, quote: q };
        (blocks[nid] = blocks[nid] || []).push(e);
        ord++;
      }
    }

    function blockOf(node, body) {
      var e = node && node.nodeType === 1 ? node : node && node.parentNode;
      while (e && e !== body) {
        if (e._bk) { return e; }
        e = e.parentNode;
      }
      return null;
    }

    function openBlock(e) {
      var b = e._bk;
      noteSheet({ id: b.id, item: b.item, at: b.at, ord: b.ord, quote: b.quote });
    }

    function card(it) {
      items[it.id] = it;
      var c = el("article", "ans-card");
      var h = el("div", "ans-head");
      h.appendChild(el("span", "ans-at", it.at));
      c.appendChild(h);
      var body = el("div", "ans content");
      body.innerHTML = it.html; // rendered and escaped by rex-tg
      scrollTables(body);
      markBlocks(body, it);
      c.appendChild(body);
      wire(body, it);
      return c;
    }

    // wire makes the blocks of body tappable.
    function wire(body, it) {
      body.addEventListener("click", function (ev) {
        if (ev.target.closest && ev.target.closest("a")) { return; }
        var s = window.getSelection && window.getSelection();
        if (s && !s.isCollapsed && String(s).trim()) { return; } // the reader is selecting
        var b = blockOf(ev.target, body);
        if (b) { openBlock(b); }
      });
      body.addEventListener("keydown", function (ev) {
        if (ev.key !== "Enter" && ev.key !== " ") { return; }
        if (ev.target._bk) { ev.preventDefault(); openBlock(ev.target); }
      });
      body._item = it;
    }

    function drawFoot() {
      foot.textContent = "";
      foot.className = "afoot";
      if (loading) {
        for (var i = 0; i < 2; i++) { foot.appendChild(el("div", "skel ans-skel")); }
        return;
      }
      if (failed) {
        foot.className = "afoot err";
        foot.appendChild(el("div", "tnote", failed));
        var again = button("Try again", "btn");
        again.addEventListener("click", function () { loadMore(); });
        foot.appendChild(again);
        return;
      }
      if (more) {
        var lm = button("Load older", "btn");
        lm.addEventListener("click", function () { loadMore(); });
        foot.appendChild(lm);
      } else if (oldest) {
        foot.appendChild(el("div", "tnote", "No older answers"));
      } else {
        var e = el("div", "empty");
        e.appendChild(el("div", "eico", "💬"));
        e.appendChild(el("div", "etitle", "No answers yet in this topic."));
        foot.appendChild(e);
      }
    }

    function loadMore() {
      if (loading || (!more && !failed)) { return; }
      loading = true;
      failed = "";
      drawFoot();
      api("api/annotate?id=" + encodeURIComponent(id) + (oldest ? "&before=" + encodeURIComponent(oldest) : ""))
        .then(function (r) {
          if (r.title && firstLoad) { setTitle(r.title); }
          firstLoad = false;
          bot = r.bot || bot;
          (r.items || []).forEach(function (it) {
            list.appendChild(card(it));
            oldest = it.id;
          });
          more = !!r.more && !!(r.items && r.items.length);
          refreshBadges();
        })
        .catch(function (e) { failed = e.message || "The answers could not be loaded."; })
        .then(function () {
          loading = false;
          drawFoot();
          watch();
        });
    }

    // Near the bottom, the next older answers load by themselves.
    var seen = null;
    if (window.IntersectionObserver) {
      seen = new IntersectionObserver(function (es) {
        if (es.some(function (x) { return x.isIntersecting; }) && !loading && more && !failed) { loadMore(); }
      }, { rootMargin: "400px 0px" });
    }
    function watch() {
      if (!seen) { return; }
      seen.unobserve(sentinel); // observing again reports the sentinel once more
      seen.observe(sentinel);
    }

    // --- a note on a selection ---
    var sel = null, hideTimer = null;
    function hidePill() { pill.hidden = true; sel = null; }
    document.addEventListener("selectionchange", function () {
      clearTimeout(hideTimer);
      hideTimer = setTimeout(function () {
        var s = window.getSelection && window.getSelection();
        if (!s || s.isCollapsed || !s.rangeCount) { hidePill(); return; }
        var text = s.toString().replace(/\s+$/, "").replace(/^\s+/, "");
        var a = s.anchorNode && (s.anchorNode.nodeType === 1 ? s.anchorNode : s.anchorNode.parentNode);
        var f = s.focusNode && (s.focusNode.nodeType === 1 ? s.focusNode : s.focusNode.parentNode);
        var ba = a && a.closest ? a.closest(".ans") : null, bf = f && f.closest ? f.closest(".ans") : null;
        if (!text || !ba || ba !== bf || !ba._item) { hidePill(); return; }
        var blk = blockOf(s.getRangeAt(0).startContainer, ba);
        sel = { item: ba._item.id, at: ba._item.at, ord: (blk ? blk._bk.ord : 0) + 0.5, quote: text };
        pill.hidden = false;
      }, 200);
    });
    pill.addEventListener("pointerdown", function (e) { e.preventDefault(); });
    pill.addEventListener("click", function () {
      if (!sel) { return; }
      var o = sel;
      hidePill();
      try { window.getSelection().removeAllRanges(); } catch (e) { /* none */ }
      noteSheet({ id: o.item + ":s" + Date.now(), item: o.item, at: o.at, ord: o.ord, quote: o.quote });
    });

    if (whole) {
      var one = { id: 1, at: (pg && pg.at) || "" };
      items[one.id] = one;
      page.className = "ans";
      markBlocks(page, one);
      wire(page, one);
      refreshBadges();
    } else {
      syncBar();
      loadMore();
    }
  }

  // --- the page ---

  var fromKb = params.get("from") === "kb";
  if (!id && goView) {
    launch(goView, params.get("t") || "");
    return;
  }
  api("api/page?id=" + encodeURIComponent(id))
    .then(function (p) {
      setTitle(p.title);
      var from = params.get("from");
      if (p.kind !== "control" && from === "panel") {
        rootBack = backToPanel;
        var back = el("button", "backlink", "‹ Panel");
        back.type = "button";
        back.setAttribute("aria-label", "Back to the control panel");
        back.addEventListener("click", backToPanel);
        title.parentNode.insertBefore(back, title);
      } else if (p.kind !== "control" && fromKb) {
        var toPanel = function () { location.href = appURL({ go: "panel" }); };
        rootBack = toPanel;
        var back2 = el("button", "backlink", "‹ Panel");
        back2.type = "button";
        back2.setAttribute("aria-label", "Back to the panel");
        back2.addEventListener("click", toPanel);
        title.parentNode.insertBefore(back2, title);
      }
      setBack(null);
      if (p.kind === "screen") { liveScreen(); return; }
      if (p.kind === "files") { fileBrowser(p.title || "Files"); return; }
      if (p.kind === "changes") { changesView(); return; }
      if (p.kind === "control") { controlPanel(); return; }
      if (p.kind === "annotate") { annotatePage(p, false); return; }
      page.className = "";
      page.innerHTML = p.html; // rendered and escaped by rex-tg
      scrollTables(page);
      if (p.kind === "answer") { annotatePage(p, true); }
    })
    .catch(function (e) {
      note(e.message || "This page could not be loaded.");
    });
})();
