/*
 * EptaAdmin visual-editing snippet.
 *
 * Usage:
 *   <script src="https://<your-eptaadmin-host>/static/visual.js" data-key="eptv_..."></script>
 *
 * What it does, always (every visitor, every page load): fetches every
 * previously-edited element for the current page and applies it over the
 * static HTML — this is what makes an edit "stick" for everyone, not just
 * an admin previewing it.
 *
 * What it does additionally, only for an admin: if the page URL contains
 * ?epta_edit=<token> (generated from EptaAdmin's "Visuel" plugin
 * dashboard) and that token verifies, it switches into edit mode — hover
 * to highlight editable text/images, click to edit/upload/delete.
 *
 * No pre-existing data-* markup is required: editable zones are detected
 * on the fly by DOM shape (a heuristic "text leaf" or an <img>), and
 * identified across page loads by a structural CSS path — see cssPath()
 * below. If the page's structure changes significantly (a redesign,
 * reordered sections), old mappings may point at the wrong element and
 * need to be redone from the live site.
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
  var originalValues = {}; // selector -> value cached before an override was applied, for instant "delete" revert
  var mappedSelectors = {}; // selector -> {type, value} for everything currently applied
  var editModeActive = false;

  function currentPagePath() {
    return location.pathname;
  }

  // --- Apply mode: runs for every visitor, edit mode or not ---

  function applyField(field) {
    var el;
    try {
      el = document.querySelector(field.selector);
    } catch (e) {
      return;
    }
    if (!el) return;
    mappedSelectors[field.selector] = field;
    if (field.type === "image") {
      if (!(field.selector in originalValues)) originalValues[field.selector] = el.getAttribute("src") || "";
      el.setAttribute("src", field.value);
    } else {
      if (!(field.selector in originalValues)) originalValues[field.selector] = el.textContent;
      el.textContent = field.value;
    }
    // Fields loaded/applied *after* edit mode already turned on (e.g. the
    // fields fetch racing the token verification) still need their delete
    // affordance — the more common ordering (fields already applied, then
    // edit mode turns on) is handled by enableEditMode's own backfill pass.
    if (editModeActive) showDeleteAffordance(el, field.selector);
  }

  function loadAndApplyFields(cb) {
    fetch(endpoint + "/fields?key=" + encodeURIComponent(key) + "&url=" + encodeURIComponent(currentPagePath()))
      .then(function (res) { return res.ok ? res.json() : []; })
      .then(function (fields) {
        (fields || []).forEach(applyField);
        if (cb) cb(fields || []);
      })
      .catch(function () { if (cb) cb([]); });
  }

  loadAndApplyFields();

  // --- Edit mode ---

  function getToken() {
    var params = new URLSearchParams(location.search);
    return params.get("epta_edit") || sessionStorage.getItem(SESSION_TOKEN_KEY) || "";
  }

  function stripTokenFromURL() {
    var params = new URLSearchParams(location.search);
    if (!params.has("epta_edit")) return;
    params.delete("epta_edit");
    var qs = params.toString();
    var newURL = location.pathname + (qs ? "?" + qs : "") + location.hash;
    history.replaceState(null, "", newURL);
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
        enableEditMode(token);
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

  function findEditable(el) {
    // Never treat our own injected UI (the toolbar, a delete affordance
    // button) as something to edit — without this, clicking a delete "✕"
    // (itself a zero-child, non-empty-text element, i.e. exactly what
    // isTextLeaf looks for) gets hijacked into a text-edit on the button
    // itself, and stopPropagation() below then keeps the button's own
    // click listener from ever seeing the click.
    if (el.closest && el.closest(".epta-visual-delete-btn, #epta-visual-toolbar")) return null;
    var node = el;
    while (node && node !== document.body) {
      if (node.tagName === "IMG") return node;
      if (isTextLeaf(node)) return node;
      node = node.parentElement;
    }
    return null;
  }

  // Structural CSS path (tag:nth-of-type chain from <body>), capped at 8
  // levels — the only way to re-identify "this same element" later
  // without any markup the site owner has to add themselves.
  function cssPath(el) {
    var parts = [];
    var node = el;
    var depth = 0;
    while (node && node !== document.body && depth < 8) {
      var tag = node.tagName.toLowerCase();
      var index = 1;
      var sib = node;
      while ((sib = sib.previousElementSibling)) {
        if (sib.tagName === node.tagName) index++;
      }
      parts.unshift(tag + ":nth-of-type(" + index + ")");
      node = node.parentElement;
      depth++;
    }
    return "body>" + parts.join(">");
  }

  function saveField(selector, type, value) {
    return fetch(endpoint + "/fields", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: JSON.stringify({ key: key, token: sessionStorage.getItem(SESSION_TOKEN_KEY), url: currentPagePath(), selector: selector, type: type, value: value }),
    });
  }

  function deleteField(selector) {
    return fetch(endpoint + "/fields", {
      method: "DELETE",
      headers: { "Content-Type": "text/plain" },
      body: JSON.stringify({ key: key, token: sessionStorage.getItem(SESSION_TOKEN_KEY), url: currentPagePath(), selector: selector }),
    });
  }

  function enableEditMode() {
    injectToolbar();
    injectStyles();
    editModeActive = true;

    // Backfill delete affordances for whatever was already mapped and
    // applied before edit mode turned on — the common case, since
    // loadAndApplyFields() always starts before the token verify
    // round-trip finishes.
    Object.keys(mappedSelectors).forEach(function (selector) {
      var el;
      try {
        el = document.querySelector(selector);
      } catch (e) {
        return;
      }
      if (el) showDeleteAffordance(el, selector);
    });

    var highlighted = null;
    document.addEventListener("mouseover", function (e) {
      var target = findEditable(e.target);
      if (target === highlighted) return;
      if (highlighted) highlighted.classList.remove("epta-visual-highlight");
      highlighted = target;
      if (highlighted) highlighted.classList.add("epta-visual-highlight");
    });

    document.addEventListener("click", function (e) {
      var target = findEditable(e.target);
      if (!target) return;
      e.preventDefault();
      e.stopPropagation();

      if (target.tagName === "IMG") {
        editImage(target);
      } else {
        editText(target);
      }
    }, true);
  }

  function editText(el) {
    var selector = cssPath(el);
    el.contentEditable = "true";
    el.classList.add("epta-visual-editing");
    el.focus();
    // Pre-select the whole element so typing replaces it outright, the
    // way a "click to edit" text field is expected to behave — done via
    // Selection/Range rather than the deprecated, inconsistently-scoped
    // document.execCommand("selectAll"), which in some browsers selects
    // the whole page instead of just this element.
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
      saveField(selector, "text", value).catch(function () {});
      showDeleteAffordance(el, selector);
    }
    el.addEventListener("blur", finish);
  }

  function editImage(el) {
    var selector = cssPath(el);
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
          return saveField(selector, "image", body.url);
        })
        .then(function () { showDeleteAffordance(el, selector); })
        .catch(function () {});
    });
    input.click();
  }

  function showDeleteAffordance(el, selector) {
    var existing = el.parentElement && el.parentElement.querySelector('[data-epta-delete-for="' + CSS.escape(selector) + '"]');
    if (existing) return;
    var btn = document.createElement("button");
    btn.textContent = "✕";
    btn.setAttribute("data-epta-delete-for", selector);
    btn.className = "epta-visual-delete-btn";
    btn.addEventListener("click", function (evt) {
      evt.preventDefault();
      evt.stopPropagation();
      deleteField(selector).then(function () {
        if (selector in originalValues) {
          if (el.tagName === "IMG") el.setAttribute("src", originalValues[selector]);
          else el.textContent = originalValues[selector];
        }
        btn.remove();
      }).catch(function () {});
    });
    if (el.style.position === "" || el.style.position === "static") el.style.position = "relative";
    el.appendChild(btn);
  }

  function injectToolbar() {
    if (document.getElementById("epta-visual-toolbar")) return;
    var bar = document.createElement("div");
    bar.id = "epta-visual-toolbar";
    bar.innerHTML =
      '<span>Mode édition EptaAdmin actif</span>' +
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
      ".epta-visual-delete-btn{position:absolute;top:-10px;right:-10px;width:20px;height:20px;" +
      "border-radius:9999px;background:#ef4444;color:#fff;border:none;font-size:11px;" +
      "line-height:20px;text-align:center;cursor:pointer;z-index:2147483647;padding:0;}";
    document.head.appendChild(style);
  }

  verifyAndMaybeEnableEditMode();
})();
