# Forever login test data

`srp-vectors.json` — SRP6 vectors produced by the server's own implementation, not by the Go code:

* generator: `tools/forevervec/srp_vectors.cpp` in this repository, built by hand against a ForeverCore checkout at
  commit `6517af05` (the build command is in the file's header comment); nothing here is built by CI.
* inputs are fixed (account name, password, 32-byte salt, client private `a`); the server class
  `BnetSRP6v2<SHA256>` / `BnetSRP6v1<SHA256>` produced `B` from the verifier the server would store, and
  `VerifyClientEvidence(A, M1)` + `CalculateServerEvidence(A, M1, S)` accepted the client evidence and returned the
  same `M2` the client computed. `serverPublicB` differs between generations of the file because the server's `b` is
  random; everything else is stable.
* what the vectors pin down: the modulus and generator, the salt byte order, the version's password derivation
  (`x`), the verifier, the padded-hash inputs (`k`, `u`), the "broken" evidence encoding of `A`, `B`, `S` and `M1`,
  and the two proofs.
