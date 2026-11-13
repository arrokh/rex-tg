// rex-tg Mini App: shows one page: an answer, the changes of a turn, the
// live screen of a topic's pane, or the files of the agent's folder.
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

  function api(path) {
    return fetch(path, { headers: { "X-Telegram-Init-Data": initData } }).then(function (r) {
      return r.json().then(function (body) {
        if (!r.ok) { throw new Error(body.error || ("HTTP " + r.status)); }
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

  // --- the file browser ---

  function fileBrowser(rootTitle) {
    var cur = "";
    if (tg && tg.BackButton) {
      tg.BackButton.onClick(function () { open(parent(cur)); });
    }

    function parent(p) {
      var i = p.lastIndexOf("/");
      return i < 0 ? "" : p.slice(0, i);
    }

    function size(n) {
      if (n < 1000) { return n + " B"; }
      if (n < 1000 * 1000) { return (n / 1000).toFixed(1) + " KB"; }
      return (n / 1000 / 1000).toFixed(1) + " MB";
    }

    function crumbs(p) {
      var nav = el("div", "crumbs");
      var root = el("a", "", rootTitle.replace(/^📂\s*/, ""));
      root.href = "#";
      root.addEventListener("click", function (e) { e.preventDefault(); open(""); });
      nav.appendChild(root);
      var acc = "";
      p.split("/").filter(Boolean).forEach(function (part) {
        acc = acc ? acc + "/" + part : part;
        var to = acc;
        nav.appendChild(el("span", "sep", " / "));
        var a = el("a", "", part);
        a.href = "#";
        a.addEventListener("click", function (e) { e.preventDefault(); open(to); });
        nav.appendChild(a);
      });
      return nav;
    }

    function open(p) {
      cur = p;
      if (tg && tg.BackButton) {
        if (p) { tg.BackButton.show(); } else { tg.BackButton.hide(); }
      }
      api("api/files?id=" + encodeURIComponent(id) + "&path=" + encodeURIComponent(p))
        .then(function (v) {
          page.className = "";
          page.textContent = "";
          page.appendChild(crumbs(v.path));
          if (v.note) { page.appendChild(el("p", "note", v.note)); }
          if (v.dir) {
            var list = el("div", "files");
            (v.entries || []).forEach(function (e) {
              var row = el("a", "entry" + (e.dir ? " dir" : ""));
              row.href = "#";
              row.appendChild(el("span", "icon", e.dir ? "📁" : "📄"));
              row.appendChild(el("span", "name", e.name));
              if (!e.dir) { row.appendChild(el("span", "size", size(e.size))); }
              row.addEventListener("click", function (ev) {
                ev.preventDefault();
                open(v.path ? v.path + "/" + e.name : e.name);
              });
              list.appendChild(row);
            });
            if (!v.entries || v.entries.length === 0) { list.appendChild(el("p", "note", "This folder is empty.")); }
            page.appendChild(list);
          } else if (v.html) {
            var body = el("div", "content");
            body.innerHTML = v.html; // rendered and escaped by rex-tg
            page.appendChild(body);
          }
          window.scrollTo(0, 0);
        })
        .catch(function (e) {
          page.className = "";
          page.textContent = "";
          page.appendChild(crumbs(p));
          page.appendChild(el("p", "note", e.message || "This file could not be loaded."));
        });
    }
    open("");
  }

  // --- the page ---

  api("api/page?id=" + encodeURIComponent(id))
    .then(function (p) {
      setTitle(p.title);
      if (p.kind === "screen") { liveScreen(); return; }
      if (p.kind === "files") { fileBrowser(p.title || "Files"); return; }
      page.className = "";
      page.innerHTML = p.html; // rendered and escaped by rex-tg
    })
    .catch(function (e) {
      note(e.message || "This page could not be loaded.");
    });
})();
