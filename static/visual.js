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

