/*
 * EptaAdmin visual-editing snippet.
 *
 * Usage:
 *   <script src="https://<your-eptaadmin-host>/static/visual.js" data-key="eptv_..."></script>
 *
 * Place this tag BEFORE your site's own application bundle/scripts. This
 * SDK works by watching the fetch() calls your own code already makes to
 * EptaAdmin's public read API (/api/v1/workspaces/.../datasources/...) to
 * load its content — if your app's script runs and fetches first, this
 * SDK never sees those calls and nothing becomes editable. Only content
 * that genuinely came from an EptaAdmin datasource, observed this way,
 * is ever editable — there is no separate storage and no markup to add
 * to your site.
 *
 * Because your site re-fetches from that same datasource on every normal
 * page load, an edit is visible to every visitor immediately — this SDK
 * does not need to (and does not) touch the page outside of edit mode.
 *
 * Edit mode activates when the page URL contains ?epta_edit=<token>,
 * generated from EptaAdmin's "Visuel" plugin dashboard.
 *
 * Known limitation: only fetch() is observed (not XMLHttpRequest), and
 * if two different cells happen to render the exact same value, clicking
 * either resolves to whichever one was observed last — both are
 * accepted trade-offs of detecting editable content without any markup.
 */
(function () {
  "use strict";

  var currentScript = document.currentScript;
  if (!currentScript) return;

  var key = currentScript.getAttribute("data-key");
  if (!key) return;

  var endpoint = currentScript.getAttribute("data-endpoint") ||
    (window.EPTA_VISUAL_ENDPOINT) ||
    (currentScript.src.replace(/\/static\/visual\.js.*$/, "") + "/api/v1/visual");

  var SESSION_TOKEN_KEY = "epta_visual_token_" + key;

  // --- Observe the site's own reads of EptaAdmin's public API ---
  // Matches /api/v1/workspaces/{wsSlug}/datasources/{tableSlug}, optionally
  // followed by /columns/{key} or /columns/{key}/{index} — the three
  // shapes handlers_api.go's public read API returns (see
  // handleAPIGetDataSource/handleAPIGetColumn/handleAPIGetColumnValue).
  var API_RE = /\/api\/v1\/workspaces\/([^/?]+)\/datasources\/([^/?]+)(?:\/columns\/([^/?]+)(?:\/(\d+))?)?/;

  // value(string) -> {wsSlug, tableSlug, column, index}
  var valueIndex = new Map();

  function recordValue(wsSlug, tableSlug, column, index, value) {
    if (value === null || value === undefined) return;
    valueIndex.set(String(value), { wsSlug: wsSlug, tableSlug: tableSlug, column: column, index: index });
  }

  function indexApiResponse(url, body) {
    var m = url.match(API_RE);
    if (!m || !body) return;
    var wsSlug = m[1], tableSlug = m[2], colFromURL = m[3], indexFromURL = m[4];
    try {
      if (colFromURL && indexFromURL !== undefined && "value" in body) {
        // .../columns/{key}/{index} -> {column, index, value}
        var idx = typeof body.index === "number" ? body.index : parseInt(indexFromURL, 10);
        recordValue(wsSlug, tableSlug, body.column || colFromURL, idx, body.value);
      } else if (colFromURL && body.values) {
        // .../columns/{key} -> {column, values: [...]}
        body.values.forEach(function (v, i) { recordValue(wsSlug, tableSlug, body.column || colFromURL, i, v); });
      } else if (body.columns) {
        // .../datasources/{tableSlug} -> {columns: {key: [...]}}
        Object.keys(body.columns).forEach(function (colKey) {
          (body.columns[colKey] || []).forEach(function (v, i) { recordValue(wsSlug, tableSlug, colKey, i, v); });
        });
      }
    } catch (e) {}
  }

  var nativeFetch = window.fetch;
  if (nativeFetch) {
    window.fetch = function () {
      var callArgs = arguments;
      var result = nativeFetch.apply(this, callArgs);
      result.then(function (res) {
        if (!res.ok) return;
        var url = typeof callArgs[0] === "string" ? callArgs[0] : (callArgs[0] && callArgs[0].url) || "";
        if (url.indexOf("/api/v1/workspaces/") === -1) return;
        res.clone().json().then(function (body) { indexApiResponse(url, body); }).catch(function () {});
      }).catch(function () {});
      return result;
    };
  }

  function refForValue(v) {
    if (v === null || v === undefined) return null;
    var s = String(v);
    return valueIndex.get(s) || valueIndex.get(s.trim()) || null;
  }

  // --- Edit mode activation (token lifecycle unchanged) ---

  function getToken() {
    var params = new URLSearchParams(location.search);
    return params.get("epta_edit") || sessionStorage.getItem(SESSION_TOKEN_KEY) || "";
  }

  function stripTokenFromURL() {
    var params = new URLSearchParams(location.search);
    if (!params.has("epta_edit")) return;
    params.delete("epta_edit");
    var qs = params.toString();
    history.replaceState(null, "", location.pathname + (qs ? "?" + qs : "") + location.hash);
  }

  function verifyAndMaybeEnableEditMode() {
    var token = getToken();
    if (!token) return;
    fetch(endpoint + "/verify", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: JSON.stringify({ key: key, token: token }),
    })
      .then(function (res) { return res.ok ? res.json() : { ok: false }; })
      .then(function (body) {
        stripTokenFromURL();
        if (!body.ok) {
          sessionStorage.removeItem(SESSION_TOKEN_KEY);
          return;
        }
        sessionStorage.setItem(SESSION_TOKEN_KEY, token);
        enableEditMode();
      })
      .catch(function () {});
  }

  var INLINE_TAGS = { A: 1, B: 1, I: 1, EM: 1, STRONG: 1, SPAN: 1, BR: 1, SMALL: 1, U: 1 };

  function isTextLeaf(el) {
    if (!el || !el.textContent || !el.textContent.trim()) return false;
    if (el.children.length === 0) return true;
    for (var i = 0; i < el.children.length; i++) {
      if (!INLINE_TAGS[el.children[i].tagName]) return false;
    }
    return true;
  }

  // Finds the nearest ancestor (including the clicked/hovered element
  // itself) that is both a plausible text/image leaf AND whose current
  // value was actually observed coming from an EptaAdmin datasource —
  // unlike a markup-based editor, nothing is editable just because it
  // looks like text.
  function findEditable(el) {
    if (el.closest && el.closest("#epta-visual-delete-btn, #epta-visual-toolbar")) return null;
    var node = el;
    var depth = 0;
    while (node && node !== document.body && depth < 12) {
      if (node.tagName === "IMG") {
        var imgRef = refForValue(node.getAttribute("src"));
        if (imgRef) return { el: node, ref: imgRef, type: "image" };
      } else if (isTextLeaf(node)) {
        var textRef = refForValue(node.textContent);
        if (textRef) return { el: node, ref: textRef, type: "text" };
      }
      node = node.parentElement;
      depth++;
    }
    return null;
  }


  function saveCell(ref, value) {
    return fetch(endpoint + "/write", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: JSON.stringify({
        key: key, token: sessionStorage.getItem(SESSION_TOKEN_KEY),
        workspaceSlug: ref.wsSlug, tableSlug: ref.tableSlug, column: ref.column, index: ref.index,
        value: value, type: "text",
      }),
    });
  }

  function clearCell(ref) {
    return fetch(endpoint + "/clear", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: JSON.stringify({
        key: key, token: sessionStorage.getItem(SESSION_TOKEN_KEY),
        workspaceSlug: ref.wsSlug, tableSlug: ref.tableSlug, column: ref.column, index: ref.index,
      }),
    });
  }

  function enableEditMode() {
    injectToolbar();
    injectStyles();
    injectDeleteButton();

    var current = null; // {el, ref, type} currently highlighted
    document.addEventListener("mouseover", function (e) {
      // Moving onto the delete button itself must not hide it — without
      // this, the pointer entering the button (which findEditable always
      // treats as "nothing", by design) immediately hides the very
      // button the person is trying to click, right before the click
      // lands.
      if (e.target.closest && e.target.closest("#epta-visual-delete-btn, #epta-visual-toolbar")) return;
      var found = findEditable(e.target);
      var foundEl = found ? found.el : null;
      if (current && current.el === foundEl) return;
      if (current) current.el.classList.remove("epta-visual-highlight");
      current = found;
      if (current) {
        current.el.classList.add("epta-visual-highlight");
        positionDeleteButton(current.el, current.ref);
      } else {
        hideDeleteButton();
      }
    });

    document.addEventListener("click", function (e) {
      var found = findEditable(e.target);
      if (!found) return;
      e.preventDefault();
      e.stopPropagation();
      if (found.type === "image") editImage(found.el, found.ref);
      else editText(found.el, found.ref);
    }, true);
  }

  function editText(el, ref) {
    el.contentEditable = "true";
    el.classList.add("epta-visual-editing");
    el.focus();
    var range = document.createRange();
    range.selectNodeContents(el);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);

    function finish() {
      el.removeEventListener("blur", finish);
      el.contentEditable = "false";
      el.classList.remove("epta-visual-editing");
      var value = el.textContent;
      saveCell(ref, value).then(function () {
        valueIndex.set(value, ref);
      }).catch(function () {});
    }
    el.addEventListener("blur", finish);
  }

  function editImage(el, ref) {
    var input = document.createElement("input");
    input.type = "file";
    input.accept = "image/*";
    input.style.display = "none";
    document.body.appendChild(input);
    input.addEventListener("change", function () {
      var file = input.files[0];
      document.body.removeChild(input);
      if (!file) return;
      var form = new FormData();
      form.append("file", file);
      form.append("key", key);
      form.append("token", sessionStorage.getItem(SESSION_TOKEN_KEY));
      fetch(endpoint + "/upload", { method: "POST", body: form })
        .then(function (res) { return res.ok ? res.json() : null; })
        .then(function (body) {
          if (!body || !body.url) return;
          el.setAttribute("src", body.url);
          return saveCell(ref, body.url).then(function () { valueIndex.set(body.url, ref); });
        })
        .catch(function () {});
    });
    input.click();
  }

  // The delete "✕" is a single reusable element positioned by coordinates
  // (getBoundingClientRect), never inserted as a child of the element
  // being edited — earlier this SDK appended it directly into the
  // target, which silently broke isTextLeaf()'s "no non-inline children"
  // check on the very next click (the button itself became an unwanted
  // child), making the just-hovered element stop looking editable right
  // before the click that was supposed to edit it.
  var deleteBtn = null;
  var deleteBtnRef = null;

  function injectDeleteButton() {
    if (deleteBtn) return;
    deleteBtn = document.createElement("button");
    deleteBtn.id = "epta-visual-delete-btn";
    deleteBtn.textContent = "✕";
    deleteBtn.style.display = "none";
    deleteBtn.addEventListener("click", function (evt) {
      evt.preventDefault();
      evt.stopPropagation();
      if (!deleteBtnRef) return;
      var ref = deleteBtnRef;
      clearCell(ref).then(function () { hideDeleteButton(); }).catch(function () {});
    });
    document.body.appendChild(deleteBtn);
  }

  function positionDeleteButton(el, ref) {
    // Top-left, not top-right: a block-level element (h1, p, div, li...)
    // fills its container's full width regardless of how short its actual
    // text is, so its right edge is frequently far from the visible
    // content — sometimes off-screen entirely. The top-left corner always
    // sits right where the element (and its content) actually starts.
    // Placed just INSIDE that corner (+2px, not straddling it with a
    // negative offset) so it never goes off-screen for content flush
    // against the page's own edge, which is common (a heading with no
    // extra margin, a page with little padding).
    var rect = el.getBoundingClientRect();
    deleteBtn.style.top = Math.max(0, rect.top + window.scrollY + 2) + "px";
    deleteBtn.style.left = Math.max(0, rect.left + window.scrollX + 2) + "px";
    deleteBtn.style.display = "block";
    deleteBtnRef = ref;
  }

  function hideDeleteButton() {
    if (!deleteBtn) return;
    deleteBtn.style.display = "none";
    deleteBtnRef = null;
  }

  function injectToolbar() {
    if (document.getElementById("epta-visual-toolbar")) return;
    var bar = document.createElement("div");
    bar.id = "epta-visual-toolbar";
    bar.innerHTML =
      "<span>Mode édition EptaAdmin actif</span>" +
      '<button type="button" id="epta-visual-exit-btn">Quitter</button>';
    document.body.appendChild(bar);
    document.getElementById("epta-visual-exit-btn").addEventListener("click", function () {
      var token = sessionStorage.getItem(SESSION_TOKEN_KEY);
      sessionStorage.removeItem(SESSION_TOKEN_KEY);
      // Revoke server-side (bumps the site's token generation, see
      // handlers_visual.go's handleVisualLogout) so this link — and any
      // other copy of it — stops working immediately instead of just
      // being forgotten by this one browser tab. Best-effort: reload
      // regardless of whether the request actually lands.
      fetch(endpoint + "/logout", {
        method: "POST",
        headers: { "Content-Type": "text/plain" },
        body: JSON.stringify({ key: key, token: token }),
        keepalive: true,
      }).catch(function () {}).finally(function () {
        location.reload();
      });
    });
  }

  function injectStyles() {
    if (document.getElementById("epta-visual-styles")) return;
    var style = document.createElement("style");
    style.id = "epta-visual-styles";
    style.textContent =
      "#epta-visual-toolbar{position:fixed;bottom:16px;right:16px;z-index:2147483647;" +
      "background:#111827;color:#f4f4f5;font:13px/1.4 -apple-system,sans-serif;" +
      "padding:10px 14px;border-radius:10px;box-shadow:0 8px 24px rgba(0,0,0,.35);" +
      "display:flex;align-items:center;gap:10px;}" +
      "#epta-visual-toolbar button{background:#34d399;color:#052e1f;border:none;" +
      "border-radius:6px;padding:5px 10px;font-weight:600;cursor:pointer;font-size:12px;}" +
      ".epta-visual-highlight{outline:2px solid #34d399 !important;outline-offset:2px;cursor:pointer;}" +
      ".epta-visual-editing{outline:2px solid #10b981 !important;outline-offset:2px;}" +
      "#epta-visual-delete-btn{position:absolute;width:20px;height:20px;" +
      "border-radius:9999px;background:#ef4444;color:#fff;border:none;font-size:11px;" +
      "line-height:20px;text-align:center;cursor:pointer;z-index:2147483647;padding:0;}";
    document.head.appendChild(style);
  }

  verifyAndMaybeEnableEditMode();
})();
