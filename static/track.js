/*
 * EptaAdmin analytics tracking snippet.
 *
 * Usage:
 *   <script src="https://<your-eptaadmin-host>/static/track.js" data-key="eptk_..."></script>
 *
 * Optional attributes:
 *   data-endpoint="https://your-own-server.example.com/collect"
 *     Send events somewhere other than this EptaAdmin instance.
 *
 * What it sends: a light browser profile (user-agent, language, screen
 * resolution), the current page URL/referrer, a per-tab session id, and
 * (on the exit event only) time spent on the page. It never reads or
 * sends the visitor's IP itself — that's captured server-side from the
 * request that delivers this payload.
 */
(function () {
  "use strict";

  var currentScript = document.currentScript;
  if (!currentScript) return;

  var key = currentScript.getAttribute("data-key");
  if (!key) return;

  // ?epta_notrack=1 on the page URL opts this visit out entirely — see
  // the "test link" button in the site's settings page. The query param
  // only survives the very first page load (any link clicked afterwards
  // drops it), so once seen it's pinned in localStorage — deliberately
  // localStorage, not sessionStorage: it must survive closing the tab (or
  // the whole browser), so a tester doesn't have to re-click the test link
  // every time they reopen the site. Unlike the per-tab session id below,
  // this has no natural expiry; clearing it means visiting the test link
  // again, or clearing the browser's site data for this origin.
  var NOTRACK_KEY = "epta_track_notrack";
  function noTrack() {
    try {
      if (new URLSearchParams(location.search).get("epta_notrack") === "1") {
        localStorage.setItem(NOTRACK_KEY, "1");
        return true;
      }
      return localStorage.getItem(NOTRACK_KEY) === "1";
    } catch (e) {
      return false;
    }
  }
  if (noTrack()) return;

  var endpoint = currentScript.getAttribute("data-endpoint") ||
    (window.EPTA_TRACK_ENDPOINT) ||
    (currentScript.src.replace(/\/static\/track\.js.*$/, "") + "/api/v1/track");

  var SESSION_KEY = "epta_track_session_id";
  function sessionId() {
    try {
      var id = sessionStorage.getItem(SESSION_KEY);
      if (!id) {
        id = Date.now().toString(36) + Math.random().toString(36).slice(2);
        sessionStorage.setItem(SESSION_KEY, id);
      }
      return id;
    } catch (e) {
      return "";
    }
  }

  function send(payload, useBeacon) {
    var body = JSON.stringify(payload);
    if (useBeacon && navigator.sendBeacon) {
      navigator.sendBeacon(endpoint, new Blob([body], { type: "text/plain" }));
      return;
    }
    try {
      fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "text/plain" },
        body: body,
        keepalive: true,
      }).catch(function () {});
    } catch (e) {}
  }

  function basePayload(eventType) {
    return {
      key: key,
      event_type: eventType,
      url: location.href,
      referrer: document.referrer || "",
      session_id: sessionId(),
      language: navigator.language || "",
      screen: screen.width + "x" + screen.height,
      timestamp: new Date().toISOString(),
    };
  }

  var enteredAt = Date.now();
  send(basePayload("enter"), false);

  var exitSent = false;
  function sendExit() {
    if (exitSent) return;
    exitSent = true;
    var payload = basePayload("exit");
    payload.time_on_page = String(Math.round((Date.now() - enteredAt) / 1000));
    send(payload, true);
  }

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "hidden") sendExit();
  });
  window.addEventListener("pagehide", sendExit);
})();
