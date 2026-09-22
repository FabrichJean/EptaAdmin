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
