# IPC Protocol Specification

This document defines the binary framing protocol used for communication between the pion-ipc Go process and its host process over stdin/stdout.

## Frame Format

All data is transmitted as length-prefixed binary frames:

```
+-------------------+--------------------+------------------+-------------+
| Length (4 bytes)  | HeaderLen (2 bytes)| Header (msgpack) | Payload     |
+-------------------+--------------------+------------------+-------------+
|    uint32 LE      |    uint16 LE       |  variable        | variable    |
```

- **Length** (4 bytes, uint32 LE): Total size of everything after this field (headerLen + header + payload).
- **HeaderLen** (2 bytes, uint16 LE): Size of the msgpack-encoded header in bytes. Maximum 65535.
- **Header**: msgpack-encoded map containing message metadata.
- **Payload**: Raw bytes immediately following the header. May be empty, msgpack-encoded data, or raw binary depending on the message type.

**Maximum frame size**: 16 MiB (the Length value must not exceed 16,777,216).

## Header Fields

The header is a msgpack map with the following fields:

| Field      | Type   | Required | Description |
|------------|--------|----------|-------------|
| `type`     | string | always   | Message type: `"req"`, `"res"`, or `"evt"` |
| `id`       | uint32 | req/res  | Request ID for correlating responses to requests |
| `method`   | string | req      | RPC method name |
| `pcId`     | string | varies   | PeerConnection identifier |
| `dcLabel`  | string | varies   | DataChannel label |
| `ok`       | bool   | res      | `true` for success, `false` for error |
| `error`    | string | res (err)| Error message when request fails |
| `event`    | string | evt      | Event name |
| `isBinary` | bool   | varies   | Indicates the payload is binary data (not text) |

**omitempty behavior**: Most fields use msgpack `omitempty` tags — zero values (`0`, `""`, `false`) are omitted from the wire encoding. Exceptions:

- **`ok`** does **not** have `omitempty`. Both `true` and `false` are always present in response headers.
- `id: 0` will not appear — request IDs should start at 1.
- `isBinary: false` will not appear — absence means text/non-binary.

## Message Types

### Request (`type: "req"`)

Sent from the host process to pion-ipc. Requires `id` and `method`. The Go process will return a response with the same `id`.

### Response (`type: "res"`)

Sent from pion-ipc to the host. The `id` field matches the originating request. `ok: true` indicates success; `ok: false` with an `error` field indicates failure.

### Event (`type: "evt"`)

Sent from pion-ipc to the host asynchronously. Contains an `event` field identifying the event name. Events have no `id` — they are fire-and-forget notifications.

## Method Reference

All methods are called via request frames from the host to pion-ipc.

### pc.create

Create a new PeerConnection.

| | |
|---|---|
| **Header** | `pcId`: unused (passed in payload) |
| **Payload** | msgpack: `{ pcId: string, iceServers: [{ urls: string[], username?: string, credential?: string }], settings?: PeerSettings }` |
| **Response** | No payload (empty) |

The `pcId` is a caller-assigned unique identifier for the PeerConnection. `iceServers` is optional (defaults to empty).

`settings` is optional. If omitted or empty, pion defaults apply. Each field is independently optional — absence means "do not call the corresponding pion setter". Duration fields are **milliseconds** (uint32). Any duration above `300000` (5 minutes) is rejected; call returns an error and no PeerConnection is created.

`PeerSettings` fields:

| Field | Type | Pion setter | Pion default |
|---|---|---|---|
| `sctpRtoMax` | uint32 (ms) | `SetSCTPRTOMax` | 60000 |
| `sctpMaxReceiveBufferSize` | uint32 (bytes) | `SetSCTPMaxReceiveBufferSize` | pion sctp package default |
| `iceDisconnectedTimeout` | uint32 (ms) | `SetICETimeouts` (arg 1) | 5000 |
| `iceFailedTimeout` | uint32 (ms) | `SetICETimeouts` (arg 2) | 25000 |
| `iceKeepAliveInterval` | uint32 (ms) | `SetICETimeouts` (arg 3) | 2000 |
| `stunGatherTimeout` | uint32 (ms) | `SetSTUNGatherTimeout` | 5000 |

**ICE timeouts merging**: pion's `SetICETimeouts` sets all three values together. If the caller specifies any of `iceDisconnectedTimeout` / `iceFailedTimeout` / `iceKeepAliveInterval`, the other two fall back to the pion defaults above.

### pc.close

Close and remove a PeerConnection and all its DataChannels.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | No payload |

### pc.createOffer

Create an SDP offer without applying it. The caller must call `pc.setLocalDescription` separately.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | msgpack: `{ sdp: string }` |

### pc.createAnswer

Create an SDP answer without applying it. The caller must call `pc.setLocalDescription` separately.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | msgpack: `{ sdp: string }` |

### pc.setRemoteDescription

Set the remote SDP description.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | msgpack: `{ type: "offer" \| "answer", sdp: string }` |
| **Response** | No payload |

### pc.setLocalDescription

Set the local SDP description explicitly (without creating an offer/answer).

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | msgpack: `{ type: "offer" \| "answer", sdp: string }` |
| **Response** | No payload |

### pc.addIceCandidate

Add a remote ICE candidate.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | msgpack: `{ candidate: string, sdpMid: string, sdpMLineIndex: uint16 }` |
| **Response** | No payload |

### pc.restartIce

Trigger an ICE restart by creating a new offer with the ICE restart flag. Does not apply it; the caller must call `pc.setLocalDescription` separately.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | msgpack: `{ sdp: string }` (the new offer SDP) |

### dc.create

Create a new DataChannel on a PeerConnection.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | msgpack: `{ label: string, ordered: bool }` |
| **Response** | No payload |

Note: `ordered` uses omitempty — `false` will be omitted from the wire, and the Go side defaults to the zero value. Callers should explicitly include this field.

### dc.send

Send data through a DataChannel.

| | |
|---|---|
| **Header** | `pcId`: required, `dcLabel`: required, `isBinary`: true if sending binary data |
| **Payload** | Raw bytes (text or binary depending on `isBinary`) |
| **Response** | msgpack: `{ bufferedAmount: uint64 }` — the SCTP buffered amount after this send |

When `isBinary` is absent/false, the payload is treated as a UTF-8 text message. When `isBinary` is true, it is sent as binary.

### dc.close

Close a DataChannel.

| | |
|---|---|
| **Header** | `pcId`: required, `dcLabel`: required |
| **Payload** | None |
| **Response** | No payload |

### dc.setBALT

Set the buffered amount low threshold for a DataChannel.

| | |
|---|---|
| **Header** | `pcId`: required, `dcLabel`: required |
| **Payload** | msgpack: `{ threshold: uint64 }` |
| **Response** | No payload |

### dc.getBA

Get the current buffered amount of a DataChannel.

| | |
|---|---|
| **Header** | `pcId`: required, `dcLabel`: required |
| **Payload** | None |
| **Response** | msgpack: `{ bufferedAmount: uint64 }` |

### pc.getSctpStats

Retrieve a snapshot of the underlying SCTP association's statistics. Returns `nil` when the association has not yet been established (e.g. before DTLS handshake completes, or after close).

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | msgpack: `SctpStats` or `nil` |

**SctpStats structure**:

| Field | Type | Unit | Description |
|---|---|---|---|
| `bytesSent` | uint64 | bytes | Cumulative bytes sent on the SCTP association |
| `bytesReceived` | uint64 | bytes | Cumulative bytes received on the SCTP association |
| `srttMs` | float64 | milliseconds | Smoothed round-trip time; `0` before first RTT measurement |
| `congestionWindow` | uint32 | bytes | Current congestion window (cwnd) |
| `receiverWindow` | uint32 | bytes | Peer's receiver window (rwnd) |
| `mtu` | uint32 | bytes | Current MTU |

**Notes**:

- The underlying fields come from pion-webrtc's `SCTPTransportStats` which itself reads pion-sctp's public accessors (`BytesSent/BytesReceived/SRTT/CWND/RWND/MTU`). All are atomic loads, so the call is cheap.
- SRTT unit conversion: pion-sctp stores ms internally, pion-webrtc scales to seconds; this method scales back to ms so callers see a straightforward number.
- `nil` response vs. zero values: a freshly created `PeerConnection` with no association returns `nil`. After the SCTP association is established, `mtu` is always non-zero (pion-sctp sets `initialMTU` at creation).
- **Not exposed**: current RTO, t3RTX backoff level (`nRtos`), inflight chunk count. These fields are not available via pion-sctp's public API in v1.8.36; expose them would require upstream changes or reflection.
- **Do not call at high frequency**. `pc.GetStats()` walks the PeerConnection's transceivers, candidates, and data channels internally. A 5-10 s sampling cadence is fine; tighter than 1 s in multi-PC scenarios is wasteful.

### ping

Health check / readiness probe. No payload required, returns an empty success response.

| | |
|---|---|
| **Header** | (no extra fields) |
| **Payload** | None |
| **Response** | No payload |

## Event Reference

All events are emitted from pion-ipc to the host. Events carry `pcId` and optionally `dcLabel` in the header to identify the source.

### pc.icecandidate

A new ICE candidate has been gathered.

| | |
|---|---|
| **Header** | `pcId`, `event: "pc.icecandidate"` |
| **Payload** | msgpack: `{ candidate: string, sdpMid: *string, sdpMLineIndex: *uint16 }` |

The `sdpMid` and `sdpMLineIndex` may be nil/null if not available.

### pc.statechange

The PeerConnection state has changed.

| | |
|---|---|
| **Header** | `pcId`, `event: "pc.statechange"` |
| **Payload** | msgpack: `{ connState: string, iceState: string }` |

`connState` maps to the W3C `RTCPeerConnectionState` values: `"new"`, `"connecting"`, `"connected"`, `"disconnected"`, `"failed"`, `"closed"`.

`iceState` is the most recent ICE connection state: `"new"`, `"checking"`, `"connected"`, `"completed"`, `"failed"`, `"disconnected"`, `"closed"`.

### pc.datachannel

A remote DataChannel has been opened by the peer.

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `event: "pc.datachannel"` |
| **Payload** | msgpack: `{ ordered: bool }` |

### dc.open

A DataChannel has opened (both local and remote channels emit this).

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `event: "dc.open"` |
| **Payload** | None |

### dc.close

A DataChannel has closed.

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `event: "dc.close"` |
| **Payload** | None |

### dc.message

A message was received on a DataChannel.

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `isBinary`, `event: "dc.message"` |
| **Payload** | Raw message bytes |

`isBinary` is true when the message is binary data (not a text string). When absent, the payload is UTF-8 text.

### dc.error

An error occurred on a DataChannel.

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `event: "dc.error"` |
| **Payload** | UTF-8 error message string (raw bytes, not msgpack) |

### dc.bufferedamountlow

The buffered amount has dropped below the threshold set by `dc.setBALT`.

| | |
|---|---|
| **Header** | `pcId`, `dcLabel`, `event: "dc.bufferedamountlow"` |
| **Payload** | None |

## Compatibility Notes

### msgpack Libraries

- **Go**: `github.com/vmihailenco/msgpack/v5`
- **JS**: `@msgpack/msgpack` (v3)

These libraries are compatible for the data types used in this protocol (strings, integers, booleans, maps, arrays, binary). Both use the standard msgpack specification.

### omitempty and Zero Values

Most fields use Go's `omitempty` tag, which omits zero values from encoding:
- `uint32` 0 is omitted — request IDs must start at 1 to avoid collisions.
- `string` "" is omitted — empty strings and absent fields are equivalent.
- `bool` false is omitted for `isBinary` — absence means text/non-binary.

**Exception**: The `ok` field does **not** have `omitempty`. It is always present in response headers (`true` or `false`), so the JS side can check `header.ok === true` directly.

### uint64 Precision in JavaScript

The `bufferedAmount` and `threshold` fields use uint64 on the Go side. JavaScript numbers (IEEE 754 double) can safely represent integers up to 2^53 - 1. Since buffered amounts in practice are well within this range, this is not a practical concern, but consumers should be aware of the theoretical limitation.

### stdout Pollution Prevention

The Go process redirects `os.Stdout` to `os.Stderr` at startup, before any other initialization. This ensures that only IPC frames are written to the real stdout pipe. All Go logging (via `slog`) and any accidental `fmt.Println` calls go to stderr. The host process can optionally collect stderr for diagnostic logging.
