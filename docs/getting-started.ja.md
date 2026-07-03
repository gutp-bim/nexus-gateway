# はじめに(Getting Started)

*[English](getting-started.md) / 日本語*

ハンズオン形式の手引きです。フルスタックを起動し、シミュレートしたコネクタから
テレメトリが流れる様子を観察し、Admin API でコネクタのライフサイクルを操作します。
物理機器なしで、約 10 分。

プロジェクトの *目的* とアーキテクチャを先に知りたい場合は
[README](../README.ja.md) を参照してください。本ガイドは一読済みを前提とします。

---

## 1. 前提ツール

| ツール | バージョン | 用途 |
|--------|-----------|------|
| Docker + Docker Compose | 最近のもの | フルスタック quickstart |
| Go | ≥ 1.25 | ゲートウェイを直接ビルド/実行 |
| `curl` + `jq` | 任意 | 以下の Admin API 例 |
| Node.js | ≥ 20 | Admin UI(ローカルビルド時のみ) |

§2〜§5 は Docker のみで動きます。§6(機器なし dev 実行)は Go が必要です。

---

## 2. フルスタックの起動

```bash
git clone https://github.com/takashikasuya/nexus-gateway
cd nexus-gateway
docker compose up --build
```

5 つのサービスが起動します:

| サービス | ポート | 内容 |
|----------|--------|------|
| `admin-ui` | http://localhost:13000 | Next.js 運用コンソール(OIDC 保護) |
| `gateway` | http://localhost:18080 | Core Agent + Admin API |
| `keycloak` | http://localhost:18090 | 運用者向け OIDC(realm `nexus-gateway`) |
| `mock-bos` | `localhost:15051` | Building OS gRPC ingress のスタブ |
| `nats` | `localhost:14222` | NATS + JetStream メッセージバス |

全サービスが healthy になるまで待ちます:

```bash
docker compose ps
```

---

## 3. ゲートウェイの稼働確認

`/health` と `/metrics` は認証不要なので、すぐ叩けます:

```bash
# ヘルススナップショット: uptime・goroutine・disk/mem・コネクタ生存性
curl -s http://localhost:18080/health | jq

# Prometheus 形式メトリクス(gateway_* / normalizer_* カウンタ)
curl -s http://localhost:18080/metrics
```

`/metrics` は ADR-0002 のベストエフォート・ドロップカウンタ 2 種を公開します:
`normalizer_invalid_total`(poison イベント)と
`normalizer_unresolved_total`(`local_id` が Point List に無いイベント)。

---

## 4. 運用者トークンの取得

主要エンドポイントはロール保護(operator/viewer)です。compose スタックでは
トークンは Keycloak から取得します。dev の `operator` ユーザーで取得:

```bash
TOKEN=$(curl -s http://localhost:18090/realms/nexus-gateway/protocol/openid-connect/token \
  -d grant_type=password \
  -d client_id=admin-ui -d client_secret=admin-ui-secret \
  -d username=operator -d password=operator | jq -r .access_token)

echo "${TOKEN:0:20}…"   # 動作確認: JWT のプレフィックスが出れば OK
```

dev 資格情報(`fixtures/keycloak/` に投入済み): `operator`/`operator`(フル操作)
と `viewer`/`viewer`(読み取り専用)。**ラボ以外へのデプロイ前に必ず変更してください**
— [SECURITY.md](../SECURITY.md) 参照。

> ブラウザ派なら http://localhost:13000 を開き `operator` でサインイン。Admin UI が
> 同じエンドポイントを代理で呼び出します。

---

## 5. テレメトリの観察とコネクタ操作

### Point List(デバイス & ポイント)を見る

```bash
curl -s http://localhost:18080/devices -H "Authorization: Bearer $TOKEN" | jq
```

各エントリは native `local_id` を canonical `point_id` に対応づけます — Normalizer が
使う join です(ADR-0001)。compose スタックでは `fixtures/point_list.json` から読み込みます。

### テレメトリの健全性を見る

```bash
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq
```

`buffer_depth` は Store-and-Forward バッファ内の**未転送**フレーム数 ＝ 送信バックログ
(ack カーソルより先の seq を持つフレーム数)であり、総行数ではありません。
`drifts` は Building OS が受理しなかったフレームの `point_id` 別カウント(Point List ⇄
twin ドリフト、ADR-0002)です。`mock-bos` 相手では両方ほぼ 0 のままになります。

### コネクタの一覧と制御

```bash
# ゲートウェイが認識しているコネクタと稼働状態
curl -s http://localhost:18080/connectors -H "Authorization: Bearer $TOKEN" | jq

# ライフサイクル操作(operator ロール): start | stop | restart | rollback
curl -s -X POST http://localhost:18080/connectors/<id>/restart \
  -H "Authorization: Bearer $TOKEN" -i

# 1 コネクタの直近コンテナログ
curl -s "http://localhost:18080/logs/<id>?tail=50" -H "Authorization: Bearer $TOKEN" | jq
```

コネクタは **署名済み OCI イメージ** として配布され、Connector Catalog 経由で
インストールされます。タグでの pull は行いません(ADR-0006)。compose スタックは
ファイルベースのカタログ(`fixtures/catalog.json`)を使い、`GET /catalog` で一覧できます。

---

## 6. ゲートウェイを直接実行(機器なし・Docker なし)

Go コードを速く回したいときは、Common Event を合成する in-process の **sim コネクタ**
付きで起動します — NATS コネクタも機器も不要:

```bash
go run ./cmd/gateway --dev-sim
```

sim の発行間隔は既定 60 秒(1 分フレッシュネスフロア)です。ローカルで素早く確認したい
場合は下げてください: `go run ./cmd/gateway --dev-sim --dev-sim-interval 5s`。

`--admin-jwks-url` が無い場合、Admin API は **認証無効**(dev 専用 — 警告ログが出ます)。
このとき `/devices`・`/telemetry`・`/connectors` はトークン不要です:

```bash
curl -s http://localhost:8080/telemetry | jq   # 注: :8080 はゲートウェイの既定ポート
```

テレメトリパイプライン(`sim → JetStream → Normalizer → Store-and-Forward`)を
端から端まで観察する最速ループです。実 NATS / Building OS / Connector Catalog へ
向ける方法は[設定フラグ](../README.ja.md)を参照。

---

## 7. 実機器の接続

2 つのシミュレータ姉妹リポジトリで、ハードウェアなしに実プロトコルコネクタを動かせます:

```bash
# OPC-UA(CI フレンドリ、plain TCP)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up

# BACnet(Who-Is/I-Am ブロードキャストのため host networking が必要)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile bacnet up
```

[`fixtures/integration/`](../fixtures/integration/README.md)、および制御経路
(Building OS → ゲートウェイ → コネクタ)は
[E2E テスト概要](e2e-test-overview.md)を参照してください。

### MQTT

MQTT コネクタは任意の MQTT 5.0 ブローカーに接続します。外部ブローカー
([Mosquitto](https://mosquitto.org/) など)が必要です。バンドルされたシミュレータはありません。

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
MQTT_POINTS='[{"topic":"sensors/room1/temp","device_ref":"mqtt://room1","unit":"Cel"}]' \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up
```

`MQTT_POINTS` の完全なスキーマ(フィールド: `topic`, `device_ref`, `unit`, `writable`,
`command_topic`, `payload_template`)は
[`connector/mqtt/connector.go`](../connector/mqtt/connector.go) を参照してください。
書き込み可能なポイントには `writable: true` に加えて `command_topic` の設定が必要です。

---

## 8. 次のステップ

- **設計を理解する** — [アーキテクチャ節](../README.ja.md)と 7 本の
  [ADR](adr/) に、すべての load-bearing な決定が記録されています。
- **ドメイン語彙** — [CONTEXT.md](../CONTEXT.md) が用語集です。用語(Connector,
  Common Event, Telemetry, Point List, …)を一貫して使ってください。
- **プロトコルコネクタを追加** — README の拡張ガイドと
  `connector/{bacnet,opcua,mqtt}` のリファレンス実装。
- **貢献する** — [CONTRIBUTING.md](../CONTRIBUTING.md) に開発ループ・テストゲート・
  PR 規約があります。

---

## トラブルシューティング

| 症状 | 想定原因 |
|------|----------|
| `/connectors`・`/devices` 等で `401 Unauthorized` | トークン未設定/期限切れ。§4 を再実行。Keycloak トークンは短命です。 |
| `POST` アクションで `403 Forbidden` | トークンが `operator` でなく `viewer`。 |
| トークン取得に失敗 | Keycloak がまだ healthy でない。`docker compose ps` で確認し、起動後に再試行。 |
| `/telemetry` の `buffer_depth` が増え続ける | Building OS へのアップリンク断。フレームがバッファ中(`mock-bos` 再起動時など想定内)。 |
| ゲートウェイがコネクタを管理できない | コンテナに host Docker socket(`/var/run/docker.sock`)のマウントが必要。`docker-compose.yml` 参照。 |
