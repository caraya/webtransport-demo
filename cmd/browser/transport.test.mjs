import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { parsePayload, runEcho } from "./web/transport.mjs";

class EchoPeer {
  static instances = [];

  constructor(url, options) {
    this.url = url;
    this.options = options;
    this.ready = Promise.resolve();
    this.closed = new Promise((resolve) => { this.finish = resolve; });
    this.incomingUnidirectionalStreams = new ReadableStream({
      start: (controller) => { this.incoming = controller; },
    });
    const datagrams = new TransformStream({
      transform: (chunk, controller) => {
        this.datagramSent = true;
        controller.enqueue(chunk);
      },
    });
    this.datagrams = {
      readable: datagrams.readable, writable: datagrams.writable, maxDatagramSize: 1200,
    };
    EchoPeer.instances.push(this);
  }

  async createBidirectionalStream() {
    return new TransformStream({
      transform(chunk, controller) {
        // Different read and write boundaries, including a non-UTF-8 byte.
        controller.enqueue(chunk.slice(0, 1));
        controller.enqueue(chunk.slice(1));
      },
    });
  }

  async createUnidirectionalStream() {
    const stream = new TransformStream();
    this.uniOpened = true;
    this.incoming.enqueue(stream.readable);
    return stream.writable;
  }

  close() {
    this.wasClosed = true;
    this.finish({ closeCode: 0 });
  }
}

describe("payload input", () => {
  test("accepts UTF-8 and exact hex bytes, rejecting malformed or oversized input", () => {
    assert.deepEqual(parsePayload("00 ff\n802a", "hex"), new Uint8Array([0, 255, 128, 42]));
    assert.deepEqual(parsePayload("hello \u4e16\u754c", "text"), new TextEncoder().encode("hello \u4e16\u754c"));
    assert.deepEqual(parsePayload("", "hex"), new Uint8Array());
    for (const invalid of ["f", "gg", "00,ff", "0x12"]) {
      assert.throws(() => parsePayload(invalid, "hex"), /hex/i);
    }
    assert.throws(() => parsePayload("x".repeat(65537), "text"), /64 KiB/);
  });
});

describe("browser WebTransport echo", () => {
  beforeEach(() => {
    EchoPeer.instances = [];
    globalThis.WebTransport = EchoPeer;
  });
  afterEach(() => { delete globalThis.WebTransport; });

  test("verifies the entire binary bidirectional echo and closes its pinned session", async () => {
    const payload = new Uint8Array([0, 255, 128, 42]);
    const pin = Buffer.alloc(32, 7).toString("base64");
    const result = await runEcho({
      url: "https://localhost:4433/echo", pin, mode: "bidirectional", payload,
    });
    assert.deepEqual(result, payload);
    const peer = EchoPeer.instances[0];
    assert.equal(peer.url, "https://localhost:4433/echo");
    assert.deepEqual(peer.options.serverCertificateHashes[0].value, new Uint8Array(32).fill(7));
    assert.equal(peer.wasClosed, true);
  });

  test("reads a unidirectional echo from the server's separate incoming stream", async () => {
    const payload = new Uint8Array([0, 255, 42]);
    assert.deepEqual(await runEcho({
      url: "https://localhost:4433/echo", mode: "unidirectional", payload,
    }), payload);
    assert.equal(EchoPeer.instances[0].uniOpened, true);
  });

  test("verifies one datagram without waiting for the datagram channel to end", async () => {
    const payload = new Uint8Array([0, 255, 42]);
    assert.deepEqual(await runEcho({
      url: "https://localhost:4433/echo", mode: "datagram", payload,
    }), payload);
    assert.equal(EchoPeer.instances[0].datagramSent, true);
    assert.equal(EchoPeer.instances[0].datagrams.writable.locked, false);
  });

  test("rejects an unknown transport mode before creating a session", async () => {
    await assert.rejects(runEcho({
      url: "https://localhost:4433/echo", mode: "invalid", payload: new Uint8Array([42]),
    }), /transport mode/i);
    assert.equal(EchoPeer.instances.length, 0);
  });

  test("rejects truncated, changed, and extra response bytes and closes the session", async () => {
    for (const response of [[255], [255, 43], [255, 42, 0]]) {
      globalThis.WebTransport = class extends EchoPeer {
        async createBidirectionalStream() {
          return {
            readable: new ReadableStream({
              start(controller) {
                controller.enqueue(new Uint8Array(response));
                controller.close();
              },
            }),
            writable: new WritableStream(),
          };
        }
      };
      await assert.rejects(runEcho({
        url: "https://localhost:4433/echo", mode: "bidirectional", payload: new Uint8Array([255, 42]),
      }), /echo/i);
      assert.equal(EchoPeer.instances.at(-1).wasClosed, true);
    }
  });

  test("times out stalled connection setup and closes the pending session", async () => {
    globalThis.WebTransport = class extends EchoPeer {
      constructor(...args) {
        super(...args);
        this.ready = new Promise(() => {});
      }
    };
    await assert.rejects(runEcho({
      url: "https://localhost:4433/echo", mode: "bidirectional", payload: new Uint8Array([42]), timeoutMs: 5,
    }), /timed out/);
    assert.equal(EchoPeer.instances[0].wasClosed, true);
  });

  test("cancels a pending session without waiting for its timeout", async () => {
    const controller = new AbortController();
    globalThis.WebTransport = class extends EchoPeer {
      constructor(...args) {
        super(...args);
        this.ready = new Promise(() => {});
      }
    };
    await assert.rejects(runEcho({
      url: "https://localhost:4433/echo", mode: "bidirectional", payload: new Uint8Array([42]),
      signal: controller.signal,
      onStatus: () => { controller.abort(new DOMException("Test canceled.", "AbortError")); },
    }), { name: "AbortError" });
    assert.equal(EchoPeer.instances[0].wasClosed, true);
  });

  test("rejects oversized datagrams before sending instead of silently dropping them", async () => {
    await assert.rejects(runEcho({
      url: "https://localhost:4433/echo", mode: "datagram", payload: new Uint8Array(1201),
    }), /negotiated datagram limit of 1200/);
    assert.equal(EchoPeer.instances[0].datagramSent, undefined);
    assert.equal(EchoPeer.instances[0].wasClosed, true);
  });

  test("rejects malformed pins before opening a connection", async () => {
    for (const pin of ["not base64!", "YWJj"]) {
      await assert.rejects(runEcho({
        url: "https://localhost:4433/echo", mode: "bidirectional", payload: new Uint8Array([42]), pin,
      }));
    }
    assert.equal(EchoPeer.instances.length, 0);
  });
});
