---
type: Concept
title: Wire and packets
description: How a byte on the world socket becomes a typed message in the agent, where a parser belongs, and the rules every parser follows.
tags: [developers, packets, protocol, wire]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:38:33Z }
sources:
  - id: buffer
    resource: internal/forever/wire/buffer.go
  - id: guid
    resource: internal/forever/wire/guid.go
  - id: opcodes
    resource: internal/forever/wire/opcodes.go
  - id: inflate
    resource: internal/forever/wire/inflate.go
  - id: worldconn
    resource: internal/forever/worldconn/frames.go
  - id: handle
    resource: internal/forever/world/session.go
  - id: mount
    resource: internal/forever/world/mount.go
  - id: packetreader
    resource: internal/packet/reader.go
  - id: opcodepkg
    resource: internal/opcode/opcode.go
  - id: makefile
    resource: Makefile
---

forever-agent sends only what a real client of the build it claims would send, and reads what the
server of that build writes. This page explains the machinery between a socket and a typed value:
the frame, the byte buffer, the opcode translation, the compression stream and the parser rules.
Use it when you add a packet, debug a refused one, or want to know why a count is bounded before an
allocation.

# The Forever path

A server frame arrives in this order:

1. `internal/forever/worldconn` reads the 16-byte header, decrypts the frame when encryption is on
   (AES-256-GCM), decompresses `SMSG_COMPRESSED_PACKET`, and translates the wire opcode.
2. The result is a `worldconn.Packet`: `Wire` (the number on the wire), `Core` (the translated
   core opcode, 0 when the core has no such number), `Name` (the core's name) and `Payload` (the
   bytes after the 4-byte opcode).
3. `internal/forever/world` dispatches it in `session.handle` with a `switch p.Core`, calls the
   feature package's parser, and merges what the parser returns into the world model or the event
   log.

Sending is the reverse: a feature package builds a payload, the session calls the connection with
the core opcode, and the connection translates the opcode, checks the size, and writes the frame.

# The frame

`wire.Header` is 16 bytes:

| Field | Size | Meaning |
|---|---|---|
| `Size` | 4 bytes, little-endian | The opcode (4 bytes) plus the payload; it does not count the header. |
| `Tag` | 12 bytes | The AES-GCM tag. Zero before encryption is switched on. |

The bounds are the server's own:

- A server frame may be at most `wire.MaxFrame` (4 MiB here). A larger declared size is an error
  before any allocation.
- A client frame is refused when `payload + 4 >= wire.MaxClientFrame` (`0x10000`): the server's
  `PacketHeader::IsValidSize` rejects frames of 64 KiB or more outright, so the agent refuses to
  build one.

# The byte buffer and GUIDs

`wire.Buffer` mirrors the server's `ByteBuffer`: a growable byte buffer with an MSB-first bit
writer and reader. Two rules surprise people:

- A numeric read or write resets the bit position, exactly as `ByteBuffer::read<T>` and `append`
  do: the pending bits are flushed first on a writer, and their remains are discarded on a reader,
  so the numeric field starts on a byte boundary.
- On a reader, failing to read sets one sticky error (`wire.ErrShort`). Later reads return zero
  values, and the parser checks the error once at the end.

GUIDs are 128-bit (`wire.GUID`: a low and a high `uint64`). The high half carries the type in bits
58–63. On the wire a GUID is packed: a 16-bit mask, then one byte per set bit. `wire.ReadPackedGUID`
and `wire.WritePackedGUID` own that form; do not hand-roll it in a parser.

# Opcode translation

The numbers are not constants of the protocol, they depend on the builds the login claimed. One
`wire.OpcodeTable` is built from the client build and the realm build (`wire.NewOpcodeTable`) and
does three jobs:

- A client opcode is translated to the core's value: an explicit override first (from the core's
  `ClassicClientOpcodes.inc`), then the group arithmetic of `ClassicOpcodes.cpp`.
- A server packet's wire number is translated back to the core's value.
- A core value is translated to the wire number for a client packet, or refused when the client
  build has none.

`wire.ServerName(core)` returns the core's name for a server opcode, and the two generated tables
(`clientopcodes_gen.go`, `servernames_gen.go`) are marked “DO NOT EDIT”: they come from the core at
a pinned commit and are regenerated, not patched. `wire.UnknownOpcode` is `0xBADD`.

A server packet whose wire number the core does not know arrives with `Core == 0` and a name like
`wire 0x4500FF`. The session counts unknown numbers instead of failing: at most 64 distinct unknown
names are kept, and the rest go into one “other unknown wire opcodes” count.

# Compression

The server compresses a payload larger than `0x400` bytes into `SMSG_COMPRESSED_PACKET`. The body
holds the inner opcode and the payload as one continuous deflate stream. The stream is created once
per connection and never reset, so packet *n* continues packet *n−1*'s stream.

`wire.Inflater` is that stream. Keep one per connection and reuse it. A packet that cannot produce
its declared size leaves the deflate position unknown, so the inflater refuses every later packet:
the connection is poisoned and must be replaced, because guessing would corrupt everything after
it. The uncompressed size is capped at 16 MiB.

# Parser rules

Every feature package follows the same rules, and a review will ask for them:

| Rule | Why |
|---|---|
| Read through a bounded buffer and check the error once at the end. | A hostile or short packet must be an error, never a panic or a slice past the end. |
| Bound every declared count and length before allocating. | Counts are server data. Several wire packages keep such bounds beside the parser (for example `death`, `mail` and `auction`). |
| Consume the payload to its last byte. | A parser that leaves bytes behind usually means the layout is wrong for this build. The wire packages have a small `exactly` helper for this. |
| Return values; do not touch the world model. | The session decides what to merge, so one parser serves several features (the loot item block appears in four packets). |
| Cite the core's file and line at each field group. | The layout is a claim about the server, and a reader must be able to check it. |

A parse failure on a packet the session needs is returned as an error and ends the session; a
packet the session can do without is counted and ignored. The difference is a decision at the
dispatch site, not inside the parser.

# Worked example: `SMSG_MOUNT_RESULT`

A mount result is one `int32`, zero for success (`SpellPackets.cpp:1156`):

```go
// internal/forever/world/mount.go
func ParseMountResult(payload []byte) (int32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("world: a mount result is 4 bytes, got %d", len(payload))
	}
	r := wire.NewReader(payload)
	res := r.ReadI32()
	if r.Err() != nil {
		return 0, fmt.Errorf("world: a mount result: %w", r.Err())
	}
	return res, nil
}
```

The dismount it belongs to sends the empty `CMSG_CANCEL_MOUNT_AURA`, which needs no builder:

```go
// internal/forever/world/mount.go
if err := s.transport().Send(wire.CMSGCancelMountAura, nil); err != nil {
	return ServeMount{}, fmt.Errorf("dismount: CMSG_CANCEL_MOUNT_AURA: %w", err)
}
```

A parser with fields follows the same shape: exact length or a bounded reader, each field read in
wire order, one error at the end, and a test that feeds it a body built by the fake server.

# Where a new packet goes

1. Put the builder and the parser in the feature package that owns the behaviour
   (`internal/forever/<feature>`), not in `internal/forever/world`. The session file should only
   grow a `case wire.SMSG…` arm and the merge.
2. Add the opcode constant to `internal/forever/wire/opcodes.go` only if the generated tables do
   not have it; the constants there are the core's own values.
3. Test it against the fake server, which builds real bodies and asserts what the client sent.
4. If the packet is a server-to-client one on the fuzz list (`internal/forever/wire`,
   `internal/forever/combat`, `internal/forever/world`), add a `Fuzz*` target or extend the seeds.
   `make fuzz` runs every target in `FUZZPKGS`; commit a crash seed with the fix.

# The 1.12 path

The older stack keeps its own codec, and it is not interchangeable with the Forever one:

| Concern | Forever | 1.12 |
|---|---|---|
| Payload codec | `internal/forever/wire.Buffer` (bit-packed, MSB-first) | `internal/packet.Reader`/`Writer` (little-endian; `ErrShort` with an offset) |
| GUID | 128-bit low/high, packed with a mask | 64-bit; the high 16 bits are the type, bits 24–47 hold an entry |
| Opcode table | Built per login from both builds | Static table generated by `tools/opcodegen` (`make opcodes`) |
| Socket | `internal/forever/worldconn` (AES-256-GCM) | `internal/worldconn` (header cipher and auth handshake) |
| Gameplay codecs | Feature packages under `internal/forever/` | `internal/game` (pure codecs), `internal/objects` (update objects) |

Both stacks follow the same parser rules. A layout copied from one to the other is a bug even when
the opcode names match.

# Related pages

- [Adding a daemon method](adding-a-daemon-method.md) — exposing a parsed value over RPC.
- [Contracts](contracts.md) — the versioned agreements above the packet layer.
- [Architecture](architecture.md) — where the session sits among the other layers.
