# Forever login test data

`srp-vectors.json` — SRP6 vectors produced by the server's own implementation, not by the Go code:

* generator: `tools/forevervec/srp_vectors.cpp` in this repository, built by hand against a ForeverCore checkout at
  commit `6517af05` (the build command is in the file's header comment); nothing here is built by CI.
* inputs are fixed (account name, password, 32-byte salt); the server class `BnetSRP6v2<SHA256>` /
  `BnetSRP6v1<SHA256>` produced `B` from the verifier the server would store, and
  `VerifyClientEvidence(A, M1)` + `CalculateServerEvidence(A, M1, S)` accepted the client evidence and returned the
  same `M2` the client computed. `serverPublicB` differs between generations of the file because the server's `b` is
  random; everything else is stable.
* two baseline vectors (`bnet-srp6-v2-sha256`, `bnet-srp6-v1-sha256`) plus, per variant, one vector per encoding
  edge the tool searches for:
  * `-a-leading-zero`, `-b-leading-zero`, `-s-leading-zero`: `A`, `B` or `S` whose modulus-size big-endian encoding
    starts with a `0x00` byte;
  * `-bit-length-multiple-of-8`: an evidence value whose bit length is a multiple of 8, which is the case in which
    `GetBrokenEvidenceVector` adds one extra leading zero byte;
  * `-m1-leading-zero`: `M1` whose first byte is `0x00`, so its number-as-hex text is 62 characters long (the client
    sends M1 as a number, like every other evidence value);
  * `-m2-leading-zero`: `M2` whose first byte is `0x00`, so the server's `BN_bn2hex` text is 62 characters long.
* the last search run: 319 attempts for v2 in 3.32 s and 1468 attempts for v1 in 0.55 s (one deterministic client
  private and one fresh random server `b` per attempt); no edge case was missing.
* every hex value except `salt` and `verifier` is written with the server's own `AsHexStr` (`BN_bn2hex`): leading
  zero bytes are dropped, but both nibbles of the first remaining byte are kept (`0x02...` prints as `02...`, not
  `2...`) — the form in which the server sends `M2` on the wire. `salt` is the raw 32-byte `ByteArrayToHexStr` and
  `verifier` the stored little-endian byte row. The Go tests therefore compare these values as numbers (`big.Int`),
  and decode `M1`/`M2` with `decodeServerEvidence`, which pads a stripped leading zero byte back to the digest length.
* what the vectors pin down: the modulus and generator, the salt byte order, the version's password derivation
  (`x`), the verifier, the padded-hash inputs (`k`, `u`), the "broken" evidence encoding of `A`, `B`, `S` and `M1`,
  the evidence hashing (including the leading-zero cases), and the two proofs.
