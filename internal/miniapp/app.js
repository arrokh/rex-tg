// rex-tg Mini App: loads one page (an answer or the changes of a turn).
// The request carries Telegram's signed init data; the server checks it.
(function () {
  var tg = window.Telegram && window.Telegram.WebApp;
  if (tg) { tg.ready(); tg.expand(); }
  var page = document.getElementById("page");
  var title = document.getElementById("title");
  var id = new URLSearchParams(location.search).get("p") || "";
  var initData = tg ? tg.initData : "";
  if (!initData) {
    page.textContent = "Open this page from its button in Telegram.";
    return;
  }
  fetch("api/page?id=" + encodeURIComponent(id), { headers: { "X-Telegram-Init-Data": initData } })
    .then(function (r) {
      return r.json().then(function (body) {
        if (!r.ok) { throw new Error(body.error || ("HTTP " + r.status)); }
        return body;
      });
    })
    .then(function (p) {
      title.textContent = p.title || "rex-tg";
      document.title = p.title || "rex-tg";
      page.className = "";
      page.innerHTML = p.html; // rendered and escaped by rex-tg
    })
    .catch(function (e) {
      page.className = "note";
      page.textContent = e.message || "This page could not be loaded.";
    });
})();
