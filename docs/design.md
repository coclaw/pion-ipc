# pion-ipc Design

## Goal

pion-ipc provides a standalone Go process that exposes [Pion](https://github.com/pion/webrtc) WebRTC capabilities over a simple IPC interface. It exists to solve a practical problem: the Node.js WebRTC ecosystem lacks a reliable, maintained native library. Rather than binding C/C++ WebRTC code via N-API or WASM, pion-ipc runs Pion in an isolated Go process and communicates through stdin/stdout, making it usable from any language runtime.

## Architecture Overview

```
Host Process (Node.js, Python, etc.)
    |
    |  stdin (host → Go): requests
    |  stdout (Go → host): responses + events
    |  stderr (Go → host): logs (optional)
    |
pion-ipc (Go binary)
    |
    └── Pion WebRTC (PeerConnections, DataChannels, ICE)
```

pion-ipc is a single Go binary. The host spawns it as a child process, writes request frames to its stdin, and reads response/event frames from its stdout. There is no network listener, no socket file, no service discovery — just pipes.

## Key Design Decisions

### Why stdin/stdout (vs Unix socket / TCP / gRPC)

- **Zero configuration**: No port allocation, no socket path conflicts, no firewall rules.
- **Automatic lifecycle**: When the parent process dies (or closes stdin), the Go process detects EOF and exits. No orphan processes.
- **Cross-platform**: Works identically on Linux, macOS, and Windows.
- **Simplicity**: Pipes are the lowest-overhead IPC mechanism available. No connection setup, no handshake, no reconnection logic.

The tradeoff is that stdin/stdout is point-to-point (single host process), which is the intended use case.

### stdout Pollution Prevention

Go code using `fmt.Println`, `log.Println`, or any other write to `os.Stdout` would corrupt the IPC stream. pion-ipc addresses this at startup by reassigning `os.Stdout` to `os.Stderr` before any other code runs. The real stdout file descriptor is captured and used exclusively by the IPC writer. All logging goes to stderr via `slog`.

This is a defensive measure that protects against accidental stdout writes from third-party dependencies (including Pion itself).

### Why msgpack (vs JSON / protobuf)

- **Binary payloads**: WebRTC DataChannel messages can be binary. JSON would require base64 encoding, adding ~33% overhead and encoding/decoding cost. msgpack natively supports binary data.
- **Compact**: Smaller on the wire than JSON for structured data.
- **Schema-free**: Unlike protobuf, no `.proto` files or code generation needed. This keeps the protocol easy to evolve.
- **Wide support**: Mature libraries exist for Go, JavaScript, Python, Rust, and most other languages.

### Why the headerLen Field

msgpack is a self-describing format, but it does not expose message boundaries — you must fully decode a value to know where it ends. By encoding the header length as a fixed 2-byte prefix, the decoder can split header bytes from payload bytes without parsing the header first. This enables:

- Forwarding raw payload bytes without decoding them.
- Efficient error handling (decode header first to identify the message, then decide whether to decode the payload).
- Clean separation between metadata (header) and application data (payload).

### Synchronous Read Loop

The service runs a single-goroutine read loop that blocks on stdin. Each frame is read, decoded, and dispatched to a handler synchronously. This is intentionally simple:

- No message reordering concerns.
- No concurrent handler execution for the same resource.
- Predictable memory usage.

The tradeoff is that a slow handler blocks subsequent reads. In practice, all handlers are fast (Pion API calls return quickly), and asynchronous events (ICE candidates, state changes, DataChannel messages) are emitted from Pion's own goroutines, bypassing the read loop entirely.

### Graceful Exit Strategy

pion-ipc exits cleanly when either:

1. **stdin EOF**: The host closes its end of the stdin pipe (normal shutdown). The read loop returns `io.EOF`, the service closes all PeerConnections, and the process exits 0.
2. **SIGTERM/SIGINT**: The context is cancelled, triggering the same cleanup path.

This dual approach ensures clean shutdown in all scenarios: normal teardown, parent process crash (broken pipe → EOF), and external kill signals.

## Core Capabilities

### Multi-PeerConnection Management

A single pion-ipc process can manage multiple concurrent PeerConnections, each identified by a caller-assigned string ID. PeerConnections are fully independent — each has its own ICE agent, DTLS transport, and set of DataChannels.

### ICE Restart with DataChannel Survival

ICE restart is supported as a first-class operation. When triggered, pion-ipc creates a new offer with the ICE restart flag, sets it as the local description, and returns the offer SDP. Existing DataChannels survive the ICE restart — they remain attached to the PeerConnection and resume data flow once the new ICE connection is established.

### Backpressure Control

DataChannels expose buffered amount monitoring:

- **getBufferedAmount**: Query the current amount of data queued for sending.
- **setBufferedAmountLowThreshold**: Set a threshold; when the buffer drains below it, a `dc.bufferedamountlow` event fires.

This allows the host to implement flow control — pause sending when the buffer is full, resume when it drains.

### Crash Isolation

Because pion-ipc runs as a separate process, a crash in the WebRTC stack (segfault in Pion, out-of-memory, etc.) does not bring down the host process. The host receives an `exit` event and can restart the Go process if needed. Conversely, a crash in the host process causes stdin EOF, triggering clean shutdown of all WebRTC resources.

## Known Limitations and Future Extensions

### DataChannel Only

The current implementation focuses exclusively on DataChannels. Audio and video tracks are not yet exposed. The architecture supports this extension — it would require new methods for track management and media negotiation, but the IPC framing and process model remain unchanged.

### Synchronous Read Loop Blocking

If a handler performs a slow operation (e.g., creating a PeerConnection with a very slow ICE server lookup), it blocks all other request processing. This has not been an issue in practice but could be addressed by dispatching handlers to a goroutine pool.

### Transport Extensibility

The stdin/stdout transport could be supplemented with TCP or Unix socket transports for use cases requiring multi-process access to the same pion-ipc instance. The framing protocol is transport-agnostic — only the read/write endpoints would change.
