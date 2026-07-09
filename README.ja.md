# nexus-gateway

[![CI](https://github.com/gutp-bim/nexus-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/gutp-bim/nexus-gateway/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**ビル設備(BMS・IoT・フィールドプロトコル)を [Building OS](https://github.com/gutp-bim/gutp-building-os-oss) に接続するエッジ統合ゲートウェイ。**

*[English](README.md) / 日本語*

> このファイルは英語の [README.md](README.md) を追従する翻訳です。設定表・エンドポイント表などの正本は英語版で、差異が生じた場合は英語版が優先されます。

> **用語:** **SBCO** (Smart Building Co-creation Organization / スマートビルディング共創機構) は本ゲートウェイが消費する Point List のスキーマを [`smartbuilding_datamodel_builder`](https://github.com/smartbuilding-co-creation-organization/smartbuilding_datamodel_builder) として定義する組織です。**Building OS** はプロビジョニングとテレメトリの System of Record となるクラウドサイドプラットフォームです。両者は **GUTP** (Green University of Tokyo Project / グリーン東大ICTプロジェクト) の一部です。

設備データを収集し、制御を中継し、プロトコル差異を吸収して、すべてを Building OS
の共通データモデルへ正規化します。Building OS が **System of Record(記録の正本)**
であり、本ゲートウェイの責務は **接続と変換** のみです。

> **ステータス: v0.1.0 public preview。** MVP のスコープ(対象/対象外)・合格条件・
> 残ギャップは **[MVP_READINESS.md](MVP_READINESS.md)** に固定しています。MVP baseline
> は OPC-UA テレメトリ/制御 + Store-and-Forward です。BACnet、edge mTLS(Traefik)本番構成、
> cosign 本番検証、BACnet COV 制御フィードバック通知は後続マイルストーンです。

---

## なぜ作ったか

> 📄 詳細な位置づけ、類似システム比較(EdgeX / Azure IoT Edge / Kura / Hono /
> ThingsBoard / EMQX Neuron / OpenRemote)、Building OS / SBCO との責務分担、
> 技術課題の分析は **[docs/background.ja.md](docs/background.ja.md)**
> ([English](docs/background.md))を参照してください。

ビルには BACnet・OPC-UA・MQTT・Modbus など多数の設備プロトコルがあり、それぞれ独自
のアドレッシングとセマンティクスを持ちます。Building OS は `(gateway_id, point_id)`
を鍵とする単一の正規 telemetry/control 契約を望みます。このプロトコル多様性をエッジ
で吸収する何かが必要です。

### なぜ EdgeX Foundry を採用しないのか

EdgeX Foundry は優れた **汎用 IoT エッジプラットフォーム** です。ビル・工場・エネル
ギー・小売・ヘルスケアを等しく対象とし、Device Service・Core Metadata・Core Command・
Application Service・Message Bus・Security スタックを備えます。最小構成でも容易に
**10〜20 コンテナ** になります。

本プロジェクトにとって、その汎用性は利点ではなくコストです。EdgeX の **Core Metadata**
(Device/Profile レジストリ、Provision Watcher)と **Core Command**(REST → Device
Service)は、**Building OS が既に所有する責務**を二重化するからです。すなわち、Digital
Twin(REC/Brick/Ditto)が機器・ポイントのレジストリであり、制御経路は Building OS →
gRPC → ゲートウェイです。EdgeX をそのまま採用すると、Building OS 側に既に存在する
レジストリと Command Service をもう一組運用することになり、これが「重い」と評価する
最大の理由です。

そのため nexus-gateway は、フル IoT プラットフォームよりも **Azure IoT Edge + プロト
コルアダプタ + gRPC アップリンク** に近い設計を意図しています。EdgeX の良い思想 ——
*Device Service 構造*・*コネクタ分離*・*Common Event → パイプライン* の流れ —— は
プラットフォームの重さを伴わずに**借用**しています。コネクタ契約は本質的に次の形です。

```
discover() → Stream[Device]
subscribe() → Stream[Telemetry]
write(cmd)  → Result
```

下回りには実績ある各プロトコル別 OSS を用います: **Eclipse Milo**(OPC-UA)、
**BACpypes3**(BACnet)、**Eclipse Paho**(MQTT)。

---

## アーキテクチャ

```
   フィールド設備 / シミュレータ
        │  BACnet/IP · OPC-UA · MQTT
        ▼
  ┌─────────────┐   evt.<proto>.<id>   ┌────────────┐  TelemetryFrame  ┌──────────────────┐
  │ Connectors  │ ───────────────────▶ │ Normalizer │ ───────────────▶ │ Store-and-Forward │
  │ (1/protocol)│   NATS JetStream     │ local_id→  │   (point_id)     │ (SQLite ring buf) │
  └─────────────┘   stream EVENTS      │  point_id  │                  └────────┬─────────┘
        ▲                              └────────────┘                            │ gRPC stream
        │ cmd.<proto>.<id>  (core NATS request-reply)                            ▼
  ┌─────────────┐        ┌──────────┐  ControlCommand  ┌────────────┐  GatewayIngress/StreamTelemetry
  │ Egress      │ ◀───── │ Dispatch │ ◀────────────────│ Building OS │ ◀─────────────────────────────
  │ agent       │  gRPC GatewayEgress/Connect          └────────────┘  (Traefik エッジで mTLS 終端)
  └─────────────┘
```

- **Connectors**(プロトコルごとに 1 つの独立コンテナ)が設備と通信し、*ネイティブ
  アドレッシングのみ* を載せた **Common Event** を発行します。正規 ID は持ちません
  ([ADR-0001](docs/adr/0001-telemetry-pipeline-shape.md))。
- **Normalizer** は `evt.>` 上の唯一の durable consumer。**Point List** を結合して
  `local_id → point_id` を解決し、**TelemetryFrame**(`gateway_id` + `point_id` +
  値 + タイムスタンプ)を発行します。
- **Store-and-Forward** は有界 SQLite リングバッファ。best-effort・drop-oldest・
  at-least-once で Building OS へ送信します
  ([ADR-0002](docs/adr/0002-best-effort-store-and-forward.md))。
- **Ingress Uplink** がフレームを Building OS の `GatewayIngress` サービスへストリーム
  し、**Egress agent** が `GatewayEgress` ストリームを保持して、受信した **Control
  Command** を、期限付き・冪等(`control_id`)な core-NATS request-reply でコネクタへ
  ディスパッチします([ADR-0004](docs/adr/0004-control-path-reliable-within-window.md))。

### 主要な設計判断(ADR)

| ADR | 決定 |
|-----|------|
| [0001](docs/adr/0001-telemetry-pipeline-shape.md) | コネクタはネイティブアドレッシングを発行し、`local_id → point_id` は Normalizer が所有。ワイヤ上の ID は `(gateway_id, point_id)` のみ。 |
| [0002](docs/adr/0002-best-effort-store-and-forward.md) | Store-and-Forward は best-effort(有界リングバッファ・drop-oldest・at-least-once)。 |
| [0003](docs/adr/0003-point-list-source-of-truth.md) | Point List の正本は Building OS twin。ゲートウェイは差分で同期。provisioning 同期 > file/CSV bootstrap。 |
| [0004](docs/adr/0004-control-path-reliable-within-window.md) | 制御は real-time-or-fail。期限付き core-NATS request-reply、`control_id` で冪等。 |
| [0005](docs/adr/0005-jetstream-topology-bounded-replay.md) | JetStream を Normalizer の前段に置き、durable な replay/back-pressure 境界とする。 |
| [0006](docs/adr/0006-connector-distribution-signed-oci.md) | コネクタは署名済み OCI イメージ、digest 固定で実行、Connector Catalog 経由で cosign 検証 + rollback。 |
| [0007](docs/adr/0007-transport-security-mtls-at-edge.md) | ゲートウェイ↔Building OS の gRPC は Building OS の Traefik エッジで mTLS 終端(`gateway_id` ↔ クライアント証明書の CN、`X-Gateway-Id` ヘッダで強制)。クラスタ内は h2c。 |

---

## 特徴

- **プロトコルコネクタ** — BACnet(Python/BACpypes3)、OPC-UA(Java/Eclipse Milo)、
  MQTT(Go/Paho)、加えて smoke 用のゼロ依存 `sim` コネクタ。各々が Building OS の
  ドメインモデルを持たない独立コンテナ。
- **Telemetry + 制御** を 1 ゲートウェイで提供。アップリンクストリーミングと書込経路
  (BACnet WriteProperty、OPC-UA Write/Method、MQTT publish)。
- **Point List 同期** — Building OS(または file/CSV スタンドイン)から差分収束で同期。
  ほぼ不変なので初回同期後はゆっくりポーリング。
- **耐障害性** — 有界 Store-and-Forward が Building OS 障害をやり過ごす。Normalizer は
  poison / point-list-miss を drop-and-meter(`normalizer_invalid_total`、
  `normalizer_unresolved_total`)。
- **セキュリティ** — Building OS への設定駆動 **TLS/mTLS**。Admin API & UI は
  **Keycloak/OIDC**(operator/viewer ロール)で保護。
- **Admin UI**(Next.js)— ダッシュボード + コネクタライフサイクル(start/stop/restart/
  upgrade)、OIDC 背後。
- **ライフサイクル管理** — Docker Engine API 経由。**署名済み OCI** によるコネクタ配布を
  Connector Catalog 経由で実施(digest 固定・cosign 検証・stop→replace→health→rollback)。

---

## クイックスタート

> 🚀 はじめての方へ：**[はじめにガイド](docs/getting-started.ja.md)**
> ([English](docs/getting-started.md))が、`compose up` からテレメトリの観察、
> Admin API でのコネクタ操作までを、機器なし・約 10 分で案内します。

```bash
# フルスタック: NATS + mock Building OS + gateway + Keycloak + Admin UI
docker compose up --build
```

| エンドポイント | URL | 備考 |
|----------------|-----|------|
| Admin UI | http://localhost:13000 | Keycloak realm `nexus-gateway`、ユーザ `operator`/`operator`、`viewer`/`viewer` |
| Gateway Admin API | http://localhost:18080 | `/health`、`/metrics`、`/connectors` |
| Keycloak | http://localhost:18090 | 管理者 `admin`/`admin` |
| mock Building OS (gRPC) | `localhost:15051` | dev 用 `GatewayIngressService` スタブ |
| NATS | `localhost:14222` | NATS クライアントポート。監視は `:18222` |

ゲートウェイバイナリを直接実行:

```bash
# 前提: JetStream 有効な NATS ブローカーが起動済みであること(ゲートウェイは起動時に
# EVENTS ストリームを作成し、接続できなければ終了する)。単体で起動するか、compose の
# 公開ポート 14222 を再利用する:
docker run --rm -p 4222:4222 nats:2.10-alpine -js        # 単体 JetStream ブローカー
go run ./cmd/gateway --dev-sim                            # 設備不要の smoke 実行(in-process sim)

# compose スタックの NATS(ホストポート 14222)を再利用する場合:
NATS_URL=nats://localhost:14222 go run ./cmd/gateway --dev-sim
```

### 設定(フラグ / 環境変数)

| フラグ | 環境変数 | 既定値 | 用途 |
|--------|----------|--------|------|
| `--version` | – | – | ゲートウェイのバージョンを表示して終了(フラグのみ) |
| `--nats` | `NATS_URL` | `nats://localhost:4222` | NATS URL |
| `--bos` | `BOS_ADDR` | `localhost:50051` | Building OS の gRPC アドレス — ingress/egress **両方**の既定値。下の2つで個別に上書き |
| `--bos-ingress-addr` | `BOS_INGRESS_ADDR` | – | Building OS **GatewayIngress** アドレス(テレメトリ)。ingress リンクで `--bos` を上書き |
| `--bos-egress-addr` | `BOS_EGRESS_ADDR` | – | Building OS **GatewayEgress** アドレス(制御プレーン)。egress リンクで `--bos` を上書き。egress は ingress と**別ポート**で終端されるため、制御パスのアドレスが異なる場合は設定しないと Control Command が接続できない |
| `--gateway-id` | `GATEWAY_ID` | `gw-001` | ゲートウェイ ID(mTLS 証明書の CN/SAN にも対応) |
| `--admin-addr` | `ADMIN_ADDR` | `:8080` | Admin API のリッスンアドレス |
| `--admin-jwks-url` | `KEYCLOAK_JWKS_URL` | – | Keycloak JWKS(空 = Admin API 認証無効) |
| `--admin-audience` | `KEYCLOAK_AUDIENCE` | `account` | 期待する JWT audience |
| `--admin-issuer` | `KEYCLOAK_ISSUER` | – | 期待する JWT issuer |
| `--point-list` | `POINT_LIST_FILE` | `fixtures/point_list.json` | ブートストラップ用フィクスチャ(provisioning ソース未設定時) |
| `--point-list-persist` | `POINT_LIST_PERSIST` | `data/point_list.json` | 同期済み Point List の永続化パス(再起動をまたぐ) |
| `--provisioning-url` | `PROVISIONING_URL` | – | Building OS の Point List provisioning API |
| `--provisioning-file` | `PROVISIONING_FILE` | – | file/CSV ベースの Point List(dev/E2E) |
| `--provisioning-connector-id` | `PROVISIONING_CONNECTOR_ID` | `bacnet-01` | `--connector-map` にエントリのないプロトコルの行に付与するフォールバック connector ID |
| `--connector-map` | `CONNECTOR_MAP` | – | `protocol:connectorID` のカンマ区切りペア。file/HTTP 両方の provisioning 経路で共通利用(例: `bacnet:bacnet-01,opcua:opcua-01,mqtt:mqtt-01`)。エントリのないプロトコルは `--provisioning-connector-id` にフォールバック |
| `--point-sync-interval` | – | `10m` | 初回同期後の Point List ポーリング間隔 |
| `--sf-db` | `SF_DB` | `data/storeforward.db` | Store-and-Forward の SQLite データベースパス |
| `--sf-cap` | `SF_CAP` | `100000` | Store-and-Forward リングバッファ容量(フレーム数)。正の値必須(それ以外は起動時に拒否) |
| `--bos-insecure` | `BOS_INSECURE` | `false` | Building OS へ平文 h2c — dev/CI のみ(ADR-0007) |
| `--bos-ca` | `BOS_CA_FILE` | – | Building OS サーバ証明書を検証する PEM CA バンドル |
| `--bos-cert` | `BOS_CERT_FILE` | – | Building OS への mTLS 用クライアント証明書 |
| `--bos-key` | `BOS_KEY_FILE` | – | Building OS への mTLS 用クライアント秘密鍵 |
| `--bos-servername` | `BOS_SERVER_NAME` | – | Building OS 証明書検証時のサーバ名を上書き |
| `--dev-sim` | `DEV_SIM` | `false` | in-process sim コネクタを起動(非本番、ADR-0001) |
| `--dev-sim-interval` | – | `60s` | `--dev-sim` の発行間隔。ローカルで素早く確認したい場合は `5s` 等に下げる |
| `--catalog-file` | `CATALOG_FILE` | – | file ベースの Connector Catalog(JSON `[]Manifest`)。`POST /connectors/{name}/install` を有効化 |
| `--catalog-url` | `CATALOG_URL` | – | リモート Connector Catalog のベース URL(`--catalog-file` を上書き) |
| `--catalog-poll-interval` | – | `10m` | Updater が新しいコネクタバージョンをカタログにポーリングする間隔(ADR-0006) |
| `--allow-adhoc-upgrade` | `ALLOW_ADHOC_UPGRADE` | `false` | dev 専用 `POST /connectors/{id}/upgrade?image=` を有効化。MVP の更新経路はカタログ駆動(ADR-0006) |
| `--catalog-allowlist` | `CATALOG_ALLOWLIST` | `ghcr.io` | 許可する OCI レジストリのカンマ区切りリスト(ADR-0006) |
| `--cosign-key` | `COSIGN_KEY_FILE` | – | コネクタイメージ署名検証用の cosign 公開鍵パス(ADR-0006)。空 = keyless |
| `--cosign-identity` | `COSIGN_IDENTITY` | – | **keyless** cosign 検証で期待する証明書 identity(ADR-0006) |
| `--cosign-oidc-issuer` | `COSIGN_OIDC_ISSUER` | – | keyless cosign 検証で期待する OIDC issuer(ADR-0006) |

> **本番(ADR-0006):** cosign フラグ(`--cosign-key`、または keyless の場合 `--cosign-identity` + `--cosign-oidc-issuer`)を設定し、コネクタイメージを install/update 前に署名検証する。未設定だと検証は無効になり起動時に警告ログが出る(ローカル/dev のみ許容)。

### 本番: Building OS への TLS/mTLS(ADR-0007)

`BOS_INSECURE=true`(平文 h2c)は **dev/CI 専用**です(エッジプロキシの無いローカル用)。
本番では `--bos-insecure` を外し、CA + クライアント証明書/鍵を渡します。gRPC リンクは
Building OS の Traefik エッジ(`TLSOption` + `passTLSClientCert`)で **mTLS 終端**され、
`gateway_id` がクライアント証明書の CN(cert-manager 発行)に束縛されます。エッジは証明書
由来の信頼ヘッダ `X-Gateway-Id` を注入し、Building OS がフレームの `gateway_id` と一致を
強制します。ゲートウェイ自身は `X-Gateway-Id` を送りません(エッジが付与)。

```bash
GATEWAY_ID=GW-SOS-001 \
BOS_ADDR=bos.example.com:443 \
BOS_CA_FILE=/etc/nexus/tls/ca.pem \
BOS_CERT_FILE=/etc/nexus/tls/gateway.crt \   # CN/SAN に GATEWAY_ID を埋め込む
BOS_KEY_FILE=/etc/nexus/tls/gateway.key \
BOS_SERVER_NAME=bos.example.com \            # 任意: SNI/検証名の上書き
PROVISIONING_URL=https://bos.example.com/provisioning \
go run ./cmd/gateway
```

- `--bos-cert`/`--bos-key` を省くと **サーバ認証のみ**(CA 検証)、付けると **mTLS**。
- CN/SAN ↔ `gateway_id` の束縛が Building OS の所有権チェックの前提です。
  [SECURITY.md](SECURITY.md) と
  [ADR-0007](docs/adr/0007-transport-security-mtls-at-edge.md) を参照。

#### Keycloak: ローカル dev 専用 — 本番は Building OS IdP を使用

`docker-compose.yml` の Keycloak は **ローカル dev / E2E / デモ専用**です
(`admin`/`admin` 認証情報、`start-dev` モード)。認証の関心事は 2 つに分かれます。

| 関心事 | 仕組み |
|--------|--------|
| 人間オペレータ (Admin UI / Admin API) | Keycloak / OIDC — Bearer JWT、`realm_access.roles` |
| Gateway ↔ Building OS 機械間認証 | **mTLS** — Keycloak は関与しない |

本番では、Gateway と Admin UI の両方を **Building OS 側の Keycloak**
(または組織共通 IdP) に向け、同梱の `keycloak` コンテナは起動しません。
Building OS の Keycloak realm に `gateway-operator` と `gateway-viewer`
の 2 つの realm role を用意するだけで済みます。本番用の環境変数例:

```env
# Gateway
KEYCLOAK_JWKS_URL=https://auth.example.com/realms/building-os/protocol/openid-connect/certs
KEYCLOAK_ISSUER=https://auth.example.com/realms/building-os
KEYCLOAK_AUDIENCE=nexus-gateway-admin-api   # "account" より専用 audience を推奨

# Admin UI
KEYCLOAK_ID=nexus-gateway-admin-ui
KEYCLOAK_SECRET=<本番シークレット>
KEYCLOAK_ISSUER=https://auth.example.com/realms/building-os
NEXTAUTH_URL=https://gateway-admin.example.com
NEXTAUTH_SECRET=<ランダムシークレット>
ADMIN_API_URL=https://gateway-admin-api.example.com
```

統合・本番環境向けの compose override として
[`docker-compose.external-keycloak.yml`](docker-compose.external-keycloak.yml)
を用意しています。

| 環境 | Keycloak |
|------|----------|
| ローカル dev / CI / E2E | 同梱 (本リポジトリ) |
| Building OS 統合環境 | Building OS 側 Keycloak |
| 本番 | Building OS 側 Keycloak または組織共通 IdP |
| Gateway ↔ Building OS | mTLS — Keycloak 不使用 |

### シミュレータ統合(設備なし)

隣接リポ `../bacnet-sim-gateway` と `../opcua-sim-gateway` が標準準拠の BACnet/IP・
OPC-UA シミュレータを提供します。詳細は
[`fixtures/integration/`](fixtures/integration/README.md):

```bash
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up
```

---

## 拡張: プロトコルコネクタの追加

コネクタは次を行う独立コンテナです。

1. Point List から、ポーリング/購読すべき **ネイティブアドレス** のみを読む。
2. **Common Event** を JetStream subject `evt.<protocol>.<connector_id>` に発行する。
   `protocol` + ネイティブ `local_id` + 値/単位/品質/タイムスタンプを載せ、**`point_id`
   は載せない**(`point_id` は Normalizer が割り当てる)。
3. `cmd.<protocol>.<connector_id>` を購読して **Control Command** を受け、型付き結果を
   `control_id` で冪等に返す。

各言語のリファレンスコネクタ(`connector/{bacnet,opcua,mqtt}`)を雛形として利用して
ください。署名済み OCI イメージとしてパッケージし、Connector Catalog に登録すると、
Core Agent が digest 固定で実行します(ADR-0006)。

NATS トピック体系・Common Event JSON スキーマ・write command の request/reply・
コンテナ env vars・Point List フォーマット・Connector Catalog マニフェスト・
冪等性ルールの完全な仕様は
**[`docs/connector-spec.md`](docs/connector-spec.md)** を参照してください。

## 拡張: Northbound 発信先の追加

正規化済みの `TelemetryFrame` は `uplink.FrameSink` インターフェース
(`Send` + `Checkpoint`)を通じて Northbound 宛先に届きます。
Building OS gRPC ストリーム(`grpcSink`)がリファレンス実装です。
MQTT ブローカー・REST エンドポイント・別の BOS など追加の宛先へ発信するには、
`FrameSink` を実装して `uplink.NewForwarder` に渡します。

インターフェース契約・`grpcSink` の実装解説・fan-out パターン・
`main.go` への組み込み方法は
**[`docs/forwarder-extension.md`](docs/forwarder-extension.md)** を参照してください。

---

## 開発

```bash
go build ./...
go test -race ./...           # Go パイプライン + コネクタ
cd admin-ui && npm run type-check && npm run build
```

CI(`.github/workflows/ci.yml`)は PR ごとに Go の build/test と Admin UI の
type-check/build を実行します。

### モジュールのシーム(テスト容易性)

ADR が定める振る舞いは **深いモジュール**(小さなインターフェース＝ユニットテスト面)に
集約され、各々が NATS/gRPC/Docker の実スタックなしで in-process に検証できます
([EP-011](docs/backlog/epic/EP-011-architecture-deepening.md)):

| モジュール | シーム | 責務 |
|------------|--------|------|
| `uplink.Forwarder` | `FrameSink`(`Send` + `Checkpoint`) | ADR-0002 のチェックポイント/前進/ドリフト/再送ポリシー。gRPC クライアントストリーミングは `grpcSink` アダプタ。 |
| `lifecycle.HealthMonitor` | `GatewayMetrics` + `ConnectorProber` | ホスト統計(uptime/mem/disk)とコンテナ生存性を分離し、各々を独立にテスト。 |
| `pointlist.Resolver` / `ReverseResolver` | 順引き + 逆引き | 単一の Point List。Normalizer(順)と制御 Dispatch(逆)が消費。 |
| `adminapi` | `NewServer` / `NewSecureServer` + `ServerOptions` | 認証なし/JWT の 2 コンストラクタを共有ビルダーで。任意ソースは 1 構造体に集約。 |

E2E テストは `integration/` にあり、ライブスタックを必要とし、関連する `E2E_*`
環境変数が未設定なら自動でスキップされます(ADR-0004)。テスト全体像(インプロセス CI
テスト・ライブコネクタスタックテスト・Building OS 統合テスト)は
[`docs/e2e-test-overview.md`](docs/e2e-test-overview.md) を参照してください。
学術評価(E1〜E6)の実験設計は
[`docs/evaluation-plan.ja.md`](docs/evaluation-plan.ja.md) を、評価テストスイートは
`test/e2e/eval_*.go`(`//go:build e2e`)を参照してください。

---

## 貢献 & セキュリティ

- **貢献** — 開発セットアップ・テストゲート・PR 規約は
  [`CONTRIBUTING.md`](CONTRIBUTING.md) に。まずは
  [はじめにガイド](docs/getting-started.ja.md)から。
- **セキュリティ** — 脆弱性は [`SECURITY.md`](SECURITY.md)(GitHub 限定アドバイザリ)
  で非公開に報告してください。公開 Issue は作成しないでください。

---

## 学術的位置づけ

本リポジトリはスマートビルディング向けエッジゲートウェイアーキテクチャの学術評価における実装アーティファクトとしても使用しています。実験設計（`docs/evaluation-plan.ja.md`）とパフォーマンステスト（`test/e2e/eval_*.go`）を再現可能性のために公開リポジトリに含めています。

## ライセンス

Apache-2.0。[`LICENSE`](LICENSE) および依存ライブラリの帰属については [`NOTICE`](NOTICE) を参照してください。
