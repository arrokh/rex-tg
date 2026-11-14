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
    location.href = "app?p=" + encodeURIComponent(p) + "&from=panel" + location.hash;
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
    document.body.classList.toggle("has-mb", !!(tg && tg.MainButton && tg.MainButton.setText));

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

    var mb = tg && tg.MainButton && tg.MainButton.setText && tg.MainButton.onClick ? tg.MainButton : null;
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
      if (t !== "new") { hideMain(); }
      document.body.classList.toggle("on-new", t === "new");
      syncBack();
      if (changed) { window.scrollTo(0, scrollAt[t] || 0); }
    }
    function hideMain() { try { if (mb) { mb.hide(); } } catch (e) { /* none */ } }
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
    window.addEventListener("pagehide", hideMain);
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
      if (a.topic) {
        var sc = button("↗", "ibtn small", "Open the topic of " + nm);
        sc.addEventListener("click", function () { hap("light"); openLink(a.topic.link); });
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
        var t = button("↗ Topic", "btn primary");
        t.addEventListener("click", function () { hap("light"); openLink(a.topic.link); });
        acts2.appendChild(t);
      } else if (a.ready) {
        var o = button("Open topic", "btn primary");
        o.addEventListener("click", function () {
          hap("light");
          post({ op: "open", pane: a.pane }, c).then(function (r) { toast(r); refresh(); });
        });
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
      var op = button("↗ Open topic", "btn primary", "Open the topic " + nm);
      op.addEventListener("click", function () { hap("light"); openLink(t.link); });
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
      var startBtn = button("", "btn primary wide" + (mb ? "" : " sticky"));
      inner.appendChild(summary);
      inner.appendChild(why);
      inner.appendChild(startBtn);

      function starters() { return (state && state.starters) || []; }
      function chosenStarter() { return starters().filter(function (x) { return x.key === agent; })[0]; }

      function updateStart() {
        var s = chosenStarter();
        var paused = !!(state && state.paused);
        var ready = !!(s && folder && !paused && !busy);
        var text = s && folder ? "Start " + s.name + " in " + folder.shown : "Start an agent";
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
        startBtn.hidden = !!mb;
        if (mb && tab === "new") {
          try {
            mb.setText(text);
            if (ready) { mb.enable(); } else { mb.disable(); }
            mb.show();
          } catch (e) { /* an older Telegram */ }
        }
      }

      function start() {
        var s = chosenStarter();
        if (!s || !folder || busy || (state && state.paused)) { return; }
        busy = true;
        updateStart();
        try { if (mb && mb.showProgress) { mb.showProgress(false); } } catch (e) { /* none */ }
        hap("light");
        post({ op: "new", agent: s.key, path: folder.path }, newView).then(function (r) {
          busy = false;
          try { if (mb && mb.hideProgress) { mb.hideProgress(); } } catch (e) { /* none */ }
          toast(r, "Progress continues in the main chat.");
          afterStart(r, s.name + " in " + folder.shown);
          updateStart();
          refresh();
        });
      }
      startBtn.addEventListener("click", start);
      if (mb) { mb.onClick(start); }

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

      function drawRows(l, err) {
        rowsBox.textContent = "";
        if (err) {
          rowsBox.appendChild(el("div", "etitle", err));
          var rt = button("Try again", "btn small");
          rt.addEventListener("click", function () { load(l ? l.path : (state.home.path)); });
          rowsBox.appendChild(rt);
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

      function load(path, keepFilter) {
        var mine = ++loading;
        if (!keepFilter) { fq = ""; fin.value = ""; }
        expl.setAttribute("aria-busy", "true");
        rowsBox.textContent = "";
        for (var i = 0; i < 4; i++) { rowsBox.appendChild(el("div", "skel row-skel")); }
        api("api/control/folders?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(path || "") + (hidden ? "&hidden=1" : ""))
          .then(function (l) {
            if (mine !== loading) { return; }
            list = l;
            drawCrumbs(l);
            drawRows(l);
            drawUse();
          })
          .catch(function (e) {
            if (mine !== loading) { return; }
            drawRows(list, e.message || "This folder could not be loaded.");
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

  // --- the page ---

  api("api/page?id=" + encodeURIComponent(id))
    .then(function (p) {
      setTitle(p.title);
      if (p.kind !== "control" && new URLSearchParams(location.search).get("from") === "panel") {
        rootBack = backToPanel;
        var back = el("button", "backlink", "‹ Panel");
        back.type = "button";
        back.setAttribute("aria-label", "Back to the control panel");
        back.addEventListener("click", backToPanel);
        title.parentNode.insertBefore(back, title);
      }
      setBack(null);
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
