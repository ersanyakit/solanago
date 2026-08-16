# token2022-init-multisend

Bu örnek, token basıldıktan sonra 5 adrese otomatik multisend yapan bir
Token-2022 yardımcı komuttur. Varsayılan 5 alıcı adresi komut içinde tanımlıdır.

```bash
go run ./examples/token2022/cmd/token2022-init-multisend \
  --keypair /absolute/path/wallet-keypair.json \
  --url devnet \
  --allow-live \
  --name DEMODEMO \
  --symbol DEMO \
  --decimals 6 \
  --amount-raw 10000000
```

Not: `--url devnet`, `--url d`, `--url testnet`, `--url t` ve `--url dev` desteklenir
(`normalizeRPCURL` kısayol map’inde).

Varsayılan alıcılar:

1. `12reyx6vYapGAjoNohg4mwRjqykzUjKpDoGssWGwtj4j`
2. `8psNvWTrdNTiVRNzAgsou9kETXNJm2SXZyaKuJraVRtf`
3. `9UnZnrFJ1CXCmCorgGU9NvYkX5np1h4v4ympx3Nrdw3v`
4. `Ee5psttzQsFjKngy3tiuiLjobxqXaUChYfj4EXtCQMGv`
5. `7nV8REk2mBVHXYJNfEJumkSLWhJhQdUtW953Whp3YYX`
