# Phonebook test programı (Go -> sBPFv3)

[Türkçe](#türkçe) | [English](#english)

## Türkçe

Bu örnek bir adres bazlı kontak listesi tutar:

- `address -> [address -> name]` şeklinde `owner` başına en fazla **20** adet kayıt.
- Her yeni kontak eklemede (update dahil) `fee-lamports` kadar SOL/lamport ücret kesilir.
- Toplanan ücret, konfigürasyondaki `treasury` hesaba gelir.
- `withdraw` ile konfigürasyon sahibi (admin) biriken tutarı çeker.

Varsayılan kayıt ücreti: `100000` lamport = `0.0001` SOL (`--fee-lamports` ile değiştirilebilir).

### 1) Programı derle

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/phonebook/phonebook.so \
  examples/phonebook/testdata/program.go
```

### 2) Programı devnet'e deploy et

```bash
go run ./cmd/go-solana keygen -o phonebook-program-keypair.json
go run ./cmd/go-solana keygen -o phonebook-payer-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair phonebook-payer-keypair.json
go run ./cmd/go-solana keygen -o phonebook-config-keypair.json
go run ./cmd/go-solana keygen -o phonebook-owner-keypair.json
go run ./cmd/go-solana keygen -o phonebook-phonebook-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair phonebook-owner-keypair.json

go run ./cmd/go-solana deploy \
  --program-id phonebook-program-keypair.json \
  --keypair phonebook-payer-keypair.json \
  --url devnet --allow-live \
  examples/phonebook/phonebook.so
```

`deploy` çıktısından program id alınır.

### 3) Config hesabını başlat

```bash
go run ./examples/phonebook/cmd/phonebook \
  init-config \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-payer-keypair.json \
  --admin-keypair phonebook-payer-keypair.json \
  --config-keypair phonebook-config-keypair.json \
  --fee-lamports 100000
```

Not: `--treasury` opsiyonel bir pubkey alanıdır. Verilmezse `--admin-keypair`'in
pubkey'i kullanılır.

### 4) Phonebook hesabı başlat

```bash
go run ./examples/phonebook/cmd/phonebook \
  init-phonebook \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-payer-keypair.json \
  --owner-keypair phonebook-owner-keypair.json \
  --phonebook-keypair phonebook-phonebook-keypair.json
```

### 5) Kişi ekle / güncelle

```bash
go run ./examples/phonebook/cmd/phonebook \
  add-contact \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-owner-keypair.json \
  --owner-keypair phonebook-owner-keypair.json \
  --config <CONFIG_PUBKEY> \
  --phonebook <PHONEBOOK_PUBKEY> \
  --address <WALLET_ADDRESS> \
  --name "Ali"
```

`add-contact` içinde aynı `address` varsa isim güncellenir; yeni kayıt sayısı 20'yi aşamaz.

### 6) Ücreti çek (withdraw)

```bash
go run ./examples/phonebook/cmd/phonebook \
  withdraw \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --admin-keypair phonebook-payer-keypair.json \
  --config <CONFIG_PUBKEY> \
  --destination <DEST_PUBKEY> \
  --amount-lamports 0
```

`amount-lamports=0` tüm bakiyeyi çekmeye çalışır.

---

## English

This example keeps an address-based contact list:

- `address -> [address -> name]`, up to **20** entries per `owner`.
- Each new contact added (including updates) is charged a fee of
  `fee-lamports` SOL/lamports.
- The collected fee goes to the `treasury` account configured at init time.
- `withdraw` lets the config owner (admin) claim the accumulated balance.

Default registration fee: `100000` lamports = `0.0001` SOL (override with
`--fee-lamports`).

### 1) Build the program

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/phonebook/phonebook.so \
  examples/phonebook/testdata/program.go
```

### 2) Deploy the program to devnet

```bash
go run ./cmd/go-solana keygen -o phonebook-program-keypair.json
go run ./cmd/go-solana keygen -o phonebook-payer-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair phonebook-payer-keypair.json
go run ./cmd/go-solana keygen -o phonebook-config-keypair.json
go run ./cmd/go-solana keygen -o phonebook-owner-keypair.json
go run ./cmd/go-solana keygen -o phonebook-phonebook-keypair.json
go run ./cmd/go-solana airdrop --url devnet --allow-live --keypair phonebook-owner-keypair.json

go run ./cmd/go-solana deploy \
  --program-id phonebook-program-keypair.json \
  --keypair phonebook-payer-keypair.json \
  --url devnet --allow-live \
  examples/phonebook/phonebook.so
```

The program id is taken from the `deploy` output.

### 3) Initialize the config account

```bash
go run ./examples/phonebook/cmd/phonebook \
  init-config \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-payer-keypair.json \
  --admin-keypair phonebook-payer-keypair.json \
  --config-keypair phonebook-config-keypair.json \
  --fee-lamports 100000
```

Note: `--treasury` is an optional pubkey field. If omitted, the
`--admin-keypair`'s pubkey is used.

### 4) Initialize the phonebook account

```bash
go run ./examples/phonebook/cmd/phonebook \
  init-phonebook \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-payer-keypair.json \
  --owner-keypair phonebook-owner-keypair.json \
  --phonebook-keypair phonebook-phonebook-keypair.json
```

### 5) Add / update a contact

```bash
go run ./examples/phonebook/cmd/phonebook \
  add-contact \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --payer phonebook-owner-keypair.json \
  --owner-keypair phonebook-owner-keypair.json \
  --config <CONFIG_PUBKEY> \
  --phonebook <PHONEBOOK_PUBKEY> \
  --address <WALLET_ADDRESS> \
  --name "Ali"
```

If the same `address` already exists in `add-contact`, the name is updated;
the entry count cannot exceed 20.

### 6) Withdraw the fee

```bash
go run ./examples/phonebook/cmd/phonebook \
  withdraw \
  --url devnet --allow-live \
  --program <PROGRAM_ID> \
  --admin-keypair phonebook-payer-keypair.json \
  --config <CONFIG_PUBKEY> \
  --destination <DEST_PUBKEY> \
  --amount-lamports 0
```

`amount-lamports=0` attempts to withdraw the entire balance.
