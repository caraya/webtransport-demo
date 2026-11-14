import { parsePayload, runEcho } from "./transport.mjs";

const form = document.querySelector("#test-form");
const controls = document.querySelector("#controls");
const cancel = document.querySelector("#cancel");
const status = document.querySelector("#status");
const error = document.querySelector("#error");
const results = document.querySelector("#results");
const support = document.querySelector("#support");
const url = document.querySelector("#url");
const pin = document.querySelector("#pin");
const mode = document.querySelector("#mode");
const format = document.querySelector("#format");
const payload = document.querySelector("#payload");
const modes = ["bidirectional", "unidirectional", "datagram"];
let active;

document.querySelector("#origin").textContent = location.origin;

function showError(message) {
  error.textContent = message;
  error.hidden = false;
  error.focus();
}

async function initialize() {
  if (!window.isSecureContext || typeof window.WebTransport !== "function") {
    support.textContent = "WebTransport is unavailable. Use a recent Chrome or Edge on http://localhost:8443 or HTTPS.";
    return;
  }
  try {
    const response = await fetch("/config.json", { cache: "no-store" });
    if (!response.ok) throw new Error(`Could not load local defaults (HTTP ${response.status}). Reload to retry.`);
    const config = await response.json();
    url.value = config.url;
    pin.value = config.certificateHash;
    support.textContent = "Browser WebTransport is available. Ready to test the endpoint.";
    controls.disabled = false;
  } catch (failure) {
    support.textContent = "Local defaults could not be loaded. Reload the page to retry.";
    showError(failure.message);
  }
}

cancel.addEventListener("click", () => {
  active?.abort(new DOMException("Test canceled.", "AbortError"));
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (active) return;
  error.hidden = true;
  payload.removeAttribute("aria-invalid");
  results.replaceChildren();
  let bytes;
  try {
    bytes = parsePayload(payload.value, format.value);
  } catch (failure) {
    payload.setAttribute("aria-invalid", "true");
    status.textContent = "Test not started. Correct the payload and try again.";
    showError(failure.message);
    return;
  }
  active = new AbortController();
  const selected = mode.value === "all" ? modes : [mode.value];
  controls.disabled = true;
  cancel.disabled = false;
  results.setAttribute("aria-busy", "true");
  let failed = false;
  try {
    for (const transportMode of selected) {
      const item = document.createElement("li");
      item.textContent = `${transportMode}: running...`;
      results.append(item);
      const started = performance.now();
      try {
        const echoed = await runEcho({
          url: url.value, pin: pin.value, mode: transportMode, payload: bytes,
          signal: active.signal,
          onStatus: (stage) => { status.textContent = `${transportMode}: ${stage.toLowerCase()}...`; },
        });
        const duration = Math.round(performance.now() - started);
        const heading = document.createElement("strong");
        heading.textContent = `PASS - ${transportMode}: ${echoed.length} bytes match (${duration} ms including setup)`;
        const preview = document.createElement("pre");
        const hex = Array.from(echoed.subarray(0, 256), (byte) => byte.toString(16).padStart(2, "0")).join(" ");
        preview.textContent = `Echo (hex): ${hex || "(empty)"}${echoed.length > 256 ? " ... (preview truncated)" : ""}`;
        item.replaceChildren(heading, preview);
      } catch (failure) {
        failed = true;
        item.textContent = `${active.signal.aborted ? "CANCELED" : "FAIL"} - ${transportMode}: ${failure.message}`;
        showError(failure.message);
        if (active.signal.aborted) break;
      }
    }
    status.textContent = active.signal.aborted ? "Tests canceled." :
      failed ? "Testing finished with failures. See results and troubleshooting below." :
      "All selected echo tests passed.";
  } finally {
    active = undefined;
    controls.disabled = false;
    cancel.disabled = true;
    results.setAttribute("aria-busy", "false");
    if (error.hidden) document.querySelector("#run").focus();
  }
});

window.addEventListener("pagehide", () => {
  active?.abort(new DOMException("Page closed.", "AbortError"));
});

await initialize();
