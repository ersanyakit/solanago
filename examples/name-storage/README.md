# Name-Storage test program (Go -> sBPFv3)

[Türkçe](#türkçe) | [English](#english)

## Türkçe

Bu klasörde test için bir örnek custom program var: `name` ve `soyad` değerini
tek bir storage hesabına yazar.

### 1) Programı derle

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/name-storage/name-storage.so \
  examples/name-storage/testdata/program.go
```

### 2) Programı devnet'e deploy et

```bash
go run ./cmd/go-solana keygen -o program-keypair.json
go run ./cmd/go-solana keygen -o payer-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair payer-keypair.json
go run ./cmd/go-solana deploy --program-id program-keypair.json --keypair payer-keypair.json --url devnet --allow-live examples/name-storage/name-storage.so
```

`deploy` çıktı JSON'undan program id alınır.

### 3) storage'a ad-soyad yaz

```bash
go run ./cmd/go-solana keygen -o storage-keypair.json
go run ./examples/name-storage/cmd/name-store \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer payer-keypair.json \
  --storage-keypair storage-keypair.json \
  --name "Ali" \
  --surname "Veli"
```

İlk çalışmada `storage-keypair` hesabı yoksa komut otomatik olarak
`storageDataLen=65` boyutunda account açar ve hesabı programına devreder.

Not:
- Bu örnek **custom** programdır, canonical SPL Token / Token-2022 programlarıyla aynı
  account formatını kullanmaz.
- Cihaz tarafında canlı kullanıcı girişi istersen, `--name` ve `--surname` boş bırakınca
  komut sizden input alır.

---

## English

This folder contains a sample custom program for testing purposes: it writes
a `name` and `surname` value into a single storage account.

### 1) Build the program

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/name-storage/name-storage.so \
  examples/name-storage/testdata/program.go
```

### 2) Deploy the program to devnet

```bash
go run ./cmd/go-solana keygen -o program-keypair.json
go run ./cmd/go-solana keygen -o payer-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair payer-keypair.json
go run ./cmd/go-solana deploy --program-id program-keypair.json --keypair payer-keypair.json --url devnet --allow-live examples/name-storage/name-storage.so
```

The program id is taken from the `deploy` output JSON.

### 3) Write a name/surname to storage

```bash
go run ./cmd/go-solana keygen -o storage-keypair.json
go run ./examples/name-storage/cmd/name-store \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer payer-keypair.json \
  --storage-keypair storage-keypair.json \
  --name "Ali" \
  --surname "Veli"
```

On the first run, if the `storage-keypair` account does not exist yet, the
command automatically opens an account of `storageDataLen=65` bytes and
assigns it to the program.

Notes:
- This is a **custom** program; it does not use the same account format as
  the canonical SPL Token / Token-2022 programs.
- If you want interactive input on the device side, leave `--name` and
  `--surname` empty and the command will prompt you for them.
