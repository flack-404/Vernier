# dist — the compiled module, as registered

This directory holds the exact bytes registered on-chain, so the registration can be
checked against a file rather than a rebuild. Everything else in this repository is
source; this is the artifact.

| | |
|---|---|
| file | `vernier.wasm` |
| sha256 | `b9904e2962bc2f62aa0adc6062da68b6e0e6eca0fa85c12f040a4559254c28a6` |
| size | 24,192,934 bytes |
| registration ID | **9** |
| tx | `0xc27c13a87834b08e60197ca2c1336b3d3a828efeada09feba69674b8990998af` |
| chain | Base Sepolia (84532) |
| IPFS | `QmQaQsjHpFWWci9BU4kVL9gixJxJXSnzznLkf1EAmio5qQ` |
| intents | `WEB_SEARCH`, `FINANCIAL_DATA` |

Three ways to confirm these are the same bytes the network was given:

```bash
# 1. this file
sha256sum dist/vernier.wasm

# 2. what IPFS serves at the registered URL
curl -sL https://gateway.pinata.cloud/ipfs/QmQaQsjHpFWWci9BU4kVL9gixJxJXSnzznLkf1EAmio5qQ | sha256sum

# 3. what the source tree builds
make rebuild && sha256sum build/vernier.wasm
```

All three give `b9904e29…54c28a6`.

The build is reproducible because the crate pins its dependencies and the MiniLM
weights are vendored in `vernier/weights/`. Note that the crate *name* participates
in the digest — renaming it permutes the WASM type section without changing
behaviour, which is why an earlier registration (ID 8) carries a different hash for
a module that scores identically. See `docs/FINDINGS.md` §11.

Registration 8 is superseded and left in place only because deregistering during the
Track 3 window is a disqualifier. **Registration 9 is the submission.**
