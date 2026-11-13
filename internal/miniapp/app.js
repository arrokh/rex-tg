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

  var scale = 1;
  function setScale(s) {
    scale = Math.round(Math.min(1.6, Math.max(0.6, s)) * 10) / 10;
    root.style.setProperty("--scale", String(scale));
    save("rex-tg.scale", scale === 1 ? null : String(scale));
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
  function setTheme(t) {
    if (t === "dark" || t === "light") { root.setAttribute("data-theme", t); } else { t = null; root.removeAttribute("data-theme"); }
    save("rex-tg.theme", t);
    themeButton.textContent = isDark() ? "☀️" : "🌙";
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

  function liveScreen() {
    page.className = "";
    page.textContent = "";
    var bar = el("div", "bar");
    var status = el("span", "status", "Loading…");
    var fit = el("button", "toggle", "Fit width");
    bar.appendChild(status);
    bar.appendChild(fit);
    var pre = el("pre", "screen");
    page.appendChild(bar);
    page.appendChild(pre);

    var fitOn = false;
    try { fitOn = localStorage.getItem("rex-tg.fit") === "1"; } catch (e) { /* no storage */ }
    function applyFit() {
      fit.classList.toggle("on", fitOn);
      if (!fitOn) { pre.style.fontSize = ""; return; }
      var cols = 1;
      pre.textContent.split("\n").forEach(function (l) { cols = Math.max(cols, l.length); });
      // A monospace character is about 0.6 of the font size wide.
      var px = Math.floor((pre.clientWidth - 16) / (cols * 0.6) * 10) / 10;
      pre.style.fontSize = Math.max(5, Math.min(13, px)) + "px";
    }
    fit.addEventListener("click", function () {
      fitOn = !fitOn;
      try { localStorage.setItem("rex-tg.fit", fitOn ? "1" : "0"); } catch (e) { /* no storage */ }
      applyFit();
    });

    var timer = null, first = true;
    function schedule() {
      clearTimeout(timer);
      timer = setTimeout(refresh, 2000);
    }
    function refresh() {
      if (document.hidden) { schedule(); return; }
      var atBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 40;
      api("api/screen?id=" + encodeURIComponent(id))
        .then(function (s) {
          if (s.error) {
            status.textContent = s.error;
            status.className = "status off";
            return;
          }
          if (pre.textContent !== s.text) {
            pre.textContent = s.text;
            applyFit();
          }
          status.textContent = "● Live · " + s.at;
          status.className = "status live";
          if (first || atBottom) { window.scrollTo(0, document.body.scrollHeight); }
          first = false;
        })
        .catch(function (e) {
          status.textContent = e.message || "Could not read the screen.";
          status.className = "status off";
        })
        .then(schedule);
    }
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) { refresh(); }
    });
    refresh();
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
