# IPC Protocol Specification

This document defines the binary framing protocol used for communication between the pion-ipc Go process and its host process (e.g., pion-node) over stdin/stdout.

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
| `ok`       | bool   | res      | `true` for success, absent (falsy) for error |
| `error`    | string | res (err)| Error message when request fails |
| `event`    | string | evt      | Event name |
| `isBinary` | bool   | varies   | Indicates the payload is binary data (not text) |

**omitempty behavior**: The Go side uses msgpack `omitempty` tags. Fields with zero values (`false`, `0`, `""`) are omitted from the wire encoding. This means:

- `ok: false` will not appear in the header — the absence of `ok` indicates failure.
- `id: 0` will not appear — request IDs should start at 1.
- `isBinary: false` will not appear — absence means text/non-binary.

## Message Types

### Request (`type: "req"`)

Sent from the host process to pion-ipc. Requires `id` and `method`. The Go process will return a response with the same `id`.

### Response (`type: "res"`)

Sent from pion-ipc to the host. The `id` field matches the originating request. `ok: true` indicates success; absence of `ok` (or `ok: false`) with an `error` field indicates failure.

### Event (`type: "evt"`)

Sent from pion-ipc to the host asynchronously. Contains an `event` field identifying the event name. Events have no `id` — they are fire-and-forget notifications.

## Method Reference

All methods are called via request frames from the host to pion-ipc.

### pc.create

Create a new PeerConnection.

| | |
|---|---|
| **Header** | `pcId`: unused (passed in payload) |
| **Payload** | msgpack: `{ pcId: string, iceServers: [{ urls: string[], username?: string, credential?: string }] }` |
| **Response** | No payload (empty) |

The `pcId` is a caller-assigned unique identifier for the PeerConnection. `iceServers` is optional (defaults to empty).

### pc.close

Close and remove a PeerConnection and all its DataChannels.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | No payload |

### pc.createOffer

Create an SDP offer and set it as the local description.

| | |
|---|---|
| **Header** | `pcId`: required |
| **Payload** | None |
| **Response** | msgpack: `{ sdp: string }` |

### pc.createAnswer

Create an SDP answer and set it as the local description.

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

Trigger an ICE restart by creating a new offer with the ICE restart flag and setting it as the local description.

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

Note: `ordered` uses omitempty — `false` will be omitted from the wire, and the Go side defaults to the zero value. The JS SDK explicitly sends this field.

### dc.send

Send data through a DataChannel.

| | |
|---|---|
| **Header** | `pcId`: required, `dcLabel`: required, `isBinary`: true if sending binary data |
| **Payload** | Raw bytes (text or binary depending on `isBinary`) |
| **Response** | No payload |

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

Go's `omitempty` tag causes zero-value fields to be omitted from encoding:
- `bool` false is omitted — consumers must treat missing `ok` as false.
- `uint32` 0 is omitted — request IDs must start at 1 to avoid collisions.
- `string` "" is omitted — empty strings and absent fields are equivalent.

The JS side must handle absent fields gracefully (e.g., `header.ok` being undefined means failure).

### uint64 Precision in JavaScript

The `bufferedAmount` and `threshold` fields use uint64 on the Go side. JavaScript numbers (IEEE 754 double) can safely represent integers up to 2^53 - 1. Since buffered amounts in practice are well within this range, this is not a practical concern, but consumers should be aware of the theoretical limitation.

### stdout Pollution Prevention

The Go process redirects `os.Stdout` to `os.Stderr` at startup, before any other initialization. This ensures that only IPC frames are written to the real stdout pipe. All Go logging (via `slog`) and any accidental `fmt.Println` calls go to stderr. The host process can optionally collect stderr for diagnostic logging.
