# pion-ipc

A standalone process that exposes WebRTC DataChannel APIs over stdin/stdout binary IPC, built on [Pion WebRTC](https://github.com/pion/webrtc).

Designed to be spawned as a child process by a Node.js (or any other) host. The host communicates via length-prefixed MessagePack frames on stdin/stdout. All log output goes to stderr.

## Build

```bash
make build        # produces bin/pion-ipc
make test         # run tests with race detector
make lint         # run golangci-lint
```

Requires Go 1.23+.

## IPC Protocol

Each frame on stdin/stdout:

```
[4 bytes: total length, uint32 LE] [2 bytes: header length, uint16 LE] [msgpack header] [raw payload]
```

"Total length" covers everything after the 4-byte prefix (header-length field + header + payload).

**Header fields** (msgpack map):

| Field      | Type   | Description                              |
|------------|--------|------------------------------------------|
| `type`     | string | `"req"`, `"res"`, or `"evt"`             |
| `id`       | uint32 | Request/response correlation ID          |
| `method`   | string | RPC method name (requests only)          |
| `pcId`     | string | PeerConnection identifier                |
| `dcLabel`  | string | DataChannel label                        |
| `ok`       | bool   | Response success flag                    |
| `error`    | string | Error message (failed responses)         |
| `event`    | string | Event name (events only)                 |
| `isBinary` | bool   | Whether payload is binary data           |

**Methods**: `pc.create`, `pc.close`, `pc.createOffer`, `pc.createAnswer`, `pc.setRemoteDescription`, `pc.setLocalDescription`, `pc.addIceCandidate`, `pc.restartIce`, `dc.create`, `dc.send`, `dc.close`, `dc.setBALT`, `dc.getBA`, `ping`

**Events**: `pc.icecandidate`, `pc.statechange`, `pc.datachannel`, `dc.open`, `dc.close`, `dc.message`, `dc.error`, `dc.bufferedamountlow`

## License

Apache-2.0
