export function parsePayload(value, format) {
  let bytes;
  if (format === "text") {
    bytes = new TextEncoder().encode(value);
  } else if (format === "hex") {
    const hex = value.replace(/\s/g, "");
    if (!/^(?:[0-9a-f]{2})*$/i.test(hex)) {
      throw new Error("Enter hex pairs such as 00 ff 80 2a, without prefixes or commas.");
    }
    if (hex.length > 131072) throw new Error("Payload must contain at most 64 KiB of bytes.");
    bytes = Uint8Array.from(hex.match(/../g) ?? [], (pair) => Number.parseInt(pair, 16));
  } else {
    throw new Error("Choose UTF-8 text or hex bytes.");
  }
  if (bytes.length > 65536) throw new Error("Payload must contain at most 64 KiB of bytes.");
  return bytes;
}

export async function runEcho({
  url, pin = "", mode, payload, signal, timeoutMs = 10000, onStatus = () => {},
}) {
  const endpoint = new URL(url);
  if (endpoint.protocol !== "https:" || endpoint.username || endpoint.password || endpoint.hash) {
    throw new Error("Use an HTTPS endpoint URL without credentials or a fragment.");
  }
  if (!(payload instanceof Uint8Array) || payload.length > 65536) {
    throw new Error("Payload must contain at most 64 KiB of bytes.");
  }
  if (!["bidirectional", "unidirectional", "datagram"].includes(mode)) {
    throw new Error("Choose a supported transport mode.");
  }
  signal?.throwIfAborted();
  const options = { allowPooling: false };
  if (pin.trim()) {
    const hash = Uint8Array.from(atob(pin.trim()), (character) => character.charCodeAt(0));
    if (hash.length !== 32) throw new Error("The SHA-256 pin must decode to exactly 32 bytes.");
    options.serverCertificateHashes = [{ algorithm: "sha-256", value: hash }];
  }
  const transport = new WebTransport(endpoint.href, options);
  let timer;
  let abort;
  const interrupted = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(
      `${mode} test timed out. Check UDP, origin, certificate, and endpoint logs; datagrams may be lost.`,
    )), timeoutMs);
    abort = () => reject(signal.reason);
    signal?.addEventListener("abort", abort, { once: true });
  });
  const closed = transport.closed.then(() => {
    throw new Error("The endpoint closed the session before the echo completed.");
  });
  const exchange = async () => {
    onStatus("Connecting");
    await transport.ready;
    onStatus("Connected; sending and verifying bytes");
    if (mode === "datagram") return echoDatagram(transport.datagrams, payload);
    if (mode === "unidirectional") {
      const writable = await transport.createUnidirectionalStream();
      const [echoed] = await Promise.all([
        readIncomingEcho(transport.incomingUnidirectionalStreams, payload),
        writePayload(writable, payload),
      ]);
      return echoed;
    }
    const stream = await transport.createBidirectionalStream();
    // Start reading before writing to avoid stream flow-control deadlocks.
    const [echoed] = await Promise.all([
      readEcho(stream.readable, payload),
      writePayload(stream.writable, payload),
    ]);
    return echoed;
  };
  try {
    return await Promise.race([exchange(), closed, interrupted]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", abort);
    transport.close();
  }
}

async function echoDatagram(datagrams, payload) {
  if (payload.length > datagrams.maxDatagramSize) {
    throw new Error(`Payload exceeds the negotiated datagram limit of ${datagrams.maxDatagramSize} bytes.`);
  }
  const reader = datagrams.readable.getReader();
  const writer = datagrams.writable.getWriter();
  try {
    const [{ value, done }] = await Promise.all([reader.read(), writer.write(payload)]);
    if (done || value.length !== payload.length ||
        value.some((byte, index) => byte !== payload[index])) {
      throw new Error("Datagram echo does not match the sent bytes.");
    }
    return value;
  } finally {
    reader.releaseLock();
    writer.releaseLock();
  }
}

async function readIncomingEcho(incoming, payload) {
  const reader = incoming.getReader();
  try {
    const { value, done } = await reader.read();
    if (done) throw new Error("The endpoint closed its incoming unidirectional streams.");
    return await readEcho(value, payload);
  } finally {
    reader.releaseLock();
  }
}

async function writePayload(writable, payload) {
  const writer = writable.getWriter();
  try {
    await writer.write(payload);
    await writer.close();
  } finally {
    writer.releaseLock();
  }
}

async function readEcho(readable, expected) {
  const reader = readable.getReader();
  const echoed = new Uint8Array(expected.length);
  let offset = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        if (offset !== expected.length) throw new Error("Echo ended before all bytes were received.");
        return echoed;
      }
      if (offset + value.length > expected.length ||
          value.some((byte, index) => byte !== expected[offset + index])) {
        throw new Error("Echo response does not match the sent bytes.");
      }
      echoed.set(value, offset);
      offset += value.length;
    }
  } finally {
    reader.releaseLock();
  }
}
