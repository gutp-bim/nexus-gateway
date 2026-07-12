# はじめに(Getting Started)

*[English](getting-started.md) / 日本語*

> このファイルは英語の [getting-started.md](getting-started.md) を追従する翻訳です。正本は英語版で、差異が生じた場合は英語版が優先されます。

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
git clone https://github.com/gutp-bim/nexus-gateway
cd nexus-gateway
docker compose up --build
```

5 つのサービスが起動します:

| サービス | ポート | 内容 |
|----------|--------|------|
| `admin-ui` | http://localhost:13000 | Next.js 運用コンソール(デフォルトは Basic 認証ログイン) |
| `gateway` | http://localhost:18080 | Core Agent + Admin API |
| `keycloak` | http://localhost:18090 | 運用者向け OIDC(realm `nexus-gateway`)— 起動はするが §4 でオプトインしない限り未使用 |
| `mock-bos` | `localhost:15051` | Building OS gRPC ingress のスタブ |
| `nats` | `localhost:14222` | NATS + JetStream メッセージバス |

全サービスが healthy になるまで待ちます:

```bash
docker compose ps
```

---

## 3. ゲートウェイの稼働確認

`/health`・`/health/live`・`/metrics` は認証不要なので、すぐ叩けます:

```bash
# レディネス: host統計 + コネクタ生存性 + 評価済み status/components。
# status は "ok" か "degraded"(いずれも HTTP 200)。degraded は不健全なサブシステム
# を名指しします(NATS断・バックログ有りで checkpoint 陳腐化・buffer 容量逼迫/書込
# エラー・Point List 空・コネクタ停止)。
curl -s http://localhost:18080/health | jq

# liveness: プロセスが応答している限り常に {"status":"ok"}。コンテナ healthcheck は
# これを対象とするので、degraded でも稼働中なら再起動されません。
curl -s http://localhost:18080/health/live | jq

# Prometheus 形式メトリクス(gateway_* / normalizer_* カウンタ)
curl -s http://localhost:18080/metrics
```

> 劣化閾値は `--health-checkpoint-stale`(既定 60s)と `--health-near-capacity-frac`
> (既定 0.9)で調整可能。バックログが空の静かなゲートウェイは degraded にフラップし
> ません(checkpoint 陳腐化はフレーム保留中のみ加算)。

`/metrics` は ADR-0002 のベストエフォート・ドロップカウンタ 2 種を公開します:
`normalizer_invalid_total`(poison イベント)と
`normalizer_unresolved_total`(`local_id` が Point List に無いイベント)。

---

## 4. Admin UI にサインイン(オプションで運用者トークンも取得)

デフォルトはシングルローカルインストール想定なので、外部 IdP を別途立てる必要は
ありません。`docker-compose.yml` は gateway の `KEYCLOAK_JWKS_URL` を未設定のままに
しており、Admin API の `/connectors`・`/devices` 等は Docker ネットワーク上では
`/health`・`/metrics` と同じく認証不要です。人間がログインする箇所は Admin UI だけです:

> http://localhost:13000 を開き、dev デフォルトの `admin`/`admin`
> (`docker-compose.yml` の `ADMIN_USERNAME`/`ADMIN_PASSWORD`)でサインインします。
> **ラボ以外で使う前に必ず `ADMIN_PASSWORD` を変更してください**
> — [SECURITY.md](../SECURITY.md) 参照。

このモードでは Admin API を直接叩くのにトークンは不要です:

```bash
curl -s http://localhost:18080/connectors | jq
```

### オプション: Keycloak SSO を使う場合

複数拠点/SSO 運用向けには、`docker-compose.yml` の `admin-ui` に
`AUTH_PROVIDER=keycloak` を設定し、`gateway`・`admin-ui` 両方の `KEYCLOAK_*` 行の
コメントを外してから(該当箇所にコメントあり)`docker compose up --build` を
再実行します。この構成では Admin API の主要エンドポイントはロール保護
(operator/viewer)になり、トークンは Keycloak から取得します。dev の
`operator` ユーザーで取得:

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

---

## 5. テレメトリの観察とコネクタ操作

以下の `-H "Authorization: Bearer $TOKEN"` は §4 で Keycloak を有効にした場合にのみ
意味を持ちます。デフォルト(Basic 認証)モードでは `$TOKEN` は未設定ですが、
Admin API 側もトークンを検査しないため、同じコマンドがどちらのモードでも動作します。

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

## 7. 自分の Device と Point を追加する(MQTT ウォークスルー)

システムインテグレータの中核タスクは、**Device** とその **Point** をオンボードする
ことです。本ウォークスルーはそれを MQTT — シミュレータ無しで手動実行できる唯一の
プロトコル — で端から端まで実施します。**Point List** エントリから可視な
**Telemetry** まで。§2 のスタックが起動済みであることを前提とします。

### Step 1 — Point List に Point を記述する

**Point List** は、各プロトコル native の `local_id` を canonical な `point_id` に
対応づける単一の source of truth です(ADR-0001)。Normalizer は受信値をこれに対して
解決します。compose スタックは
[`fixtures/point_list.json`](../fixtures/point_list.json)(`POINT_LIST_FILE`)から
読み込みます。

新しい Device 上の新しい Point のエントリを 1 つ追加します:

```jsonc
{
  "connector_id": "mqtt-01",              // この Point を所有する Connector
  "protocol": "mqtt",
  "local_id": "sensors/lobby/temp",        // プロトコル native アドレス — MQTT トピック
  "point_id": "lobby_temperature",         // 下流すべてで使う canonical id
  "device_ref": "mqtt://lobby-ahu",        // Point を 1 つの論理 Device にまとめる
  "unit": "Cel",
  "writable": false                        // 読み取り専用 Point(command topic 不要)
}
```

- **`point_id`** は telemetry と control が使う安定した canonical 識別子。プロトコル
  アドレッシングは一切持ちません。
- **`local_id`** は Connector が読むプロトコル native アドレス。MQTT では購読する
  トピックです。
- **`device_ref`** は Point を 1 つの **Device** にまとめます。同じ値を共有する
  エントリは `/devices` で同一 Device として現れます。
- **`writable`** はその Point が制御書き込みを受け付けるかを示します(書き込み可能な
  MQTT Point には `command_topic` も必要 — 下記スキーマポインタ参照)。

### Step 2 — MQTT Connector をそこに向ける

`MQTT_POINTS` エントリ(その `topic` は Point List の `local_id` と一致必須)付きで
MQTT Connector を起動します。`MQTT_BROKER_URL` は到達可能な任意の MQTT 5.0 ブローカー
(例: ローカルの [Mosquitto](https://mosquitto.org/))に向けます:

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
MQTT_POINTS='[{"topic":"sensors/lobby/temp","device_ref":"mqtt://lobby-ahu","unit":"Cel"}]' \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up -d mqtt-connector
```

### Step 3 — 値を publish する

トピックに値を publish します(コネクタが Common Event に正規化します):

```bash
mosquitto_pub -h your-broker -t sensors/lobby/temp -m '21.4'
```

### Step 4 — Telemetry の到達を確認する

新しい Device と Point が現れ、値が Telemetry まで流れます:

```bash
# 新しい Device(device_ref)とその Point(point_id)が Point List から解決される
curl -s http://localhost:18080/devices -H "Authorization: Bearer $TOKEN" | jq

# mock-bos 相手では buffer_depth / drifts はほぼ 0。buffer_depth が増え続ける場合は
# 値は受理されたがアップリンクが断(トラブルシューティング参照)
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq
```

`/devices` に `point_id` が出ない場合、ゲートウェイが編集済み Point List を読み込んで
いません(`docker compose restart gateway` で再起動)。Point は出るが値が流れない
場合は、`MQTT_POINTS` の `topic` が `local_id` と一致していません。

---

## 8. 実機器の接続

2 つのシミュレータ姉妹リポジトリで、ハードウェアなしに実プロトコルコネクタを動かせます。
これらは**本リポジトリの隣にチェックアウトする必要がある別リポジトリ**です — 同じ親
ディレクトリの下に sibling として clone します(compose のビルドコンテキストは
`../bacnet-sim-gateway` と `../opcua-sim-gateway`):

```bash
# nexus-gateway/ を既に含む親ディレクトリで
git clone https://github.com/takashikasuya/bacnet-sim-gateway
git clone https://github.com/takashikasuya/opcua-sim-gateway
```

sibling ディレクトリが無い場合、`docker compose … --profile opcua up` はプロトコル
固有のエラーではなくビルドコンテキストエラー(`../opcua-sim-gateway: no such file or
directory`)で失敗します — sibling を clone して再試行してください。

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

MQTT の compose パスは**バンドル済み**です — Mosquitto ブローカーとサンプル
パブリッシャがコネクタと一緒に起動するので、外部インフラは不要です
(BACnet/OPC-UA のシミュレータ相当):

```bash
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up --build
```

`mqtt-broker`・読み取りポイント(`sensors/room1/temp`)を 10 秒毎に publish する
`mqtt-publisher`・`mqtt-connector` が追加されます。MQTT ポイントは 2 つとも
`fixtures/point_list.json` に定義済み: `room1_temperature`(読み取り専用)と
`room1_setpoint`(書き込み可能)。テレメトリの到達を確認:

```bash
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq   # バッファ流入
curl -s http://localhost:18080/devices   -H "Authorization: Bearer $TOKEN" | jq   # room1 ポイント
```

**書き込み可能**ポイントを Command Channel 経由で操作(パブリッシャが setpoint の
command topic を購読し、受信した書き込みを自身のログにエコーします):

```bash
# room1_setpoint に制御を送り、ブローカーへの到達を確認
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml logs -f mqtt-publisher
```

**バンドルではなく外部ブローカーを使う場合:** `MQTT_BROKER_URL` を上書きして
コネクタのみ起動:

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up mqtt-connector
```

`MQTT_POINTS` の完全な JSON スキーマは
[`cmd/mqtt-connector/main.go`](../cmd/mqtt-connector/main.go) の `pointEnv` 構造体を
参照してください — ここが wire キー(`topic`, `device_ref`, `unit`, `writable`,
`command_topic`, `payload_template`)を定義します。書き込み可能なポイントには
`writable: true` に加えて `command_topic` の設定が必要です。

---

## 9. 次のステップ

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
| Admin UI で `401 Unauthorized` | `ADMIN_USERNAME`/`ADMIN_PASSWORD` が違う(Basic 認証モード)。Keycloak を有効にしている場合はトークン未設定/期限切れ — §4 を再実行。 |
| `/connectors`・`/devices` 等で `401 Unauthorized` | Keycloak モードでのみ起こり得ます(デフォルトモードではこれらは認証不要)。トークン未設定/期限切れ — §4 を再実行。Keycloak トークンは短命です。 |
| `POST` アクションで `403 Forbidden` | Keycloak モードのみ: トークンが `operator` でなく `viewer`。 |
| トークン取得で `unauthorized_client` | パスワードグラント用に realm の `admin-ui` クライアントで**ダイレクトアクセスグラント**を有効化する必要があります。同梱の dev realm(`fixtures/keycloak/realm.json`)は有効化済み。realm を独自変更した場合は再度有効化してください。 |
| ブラウザサインインで `Invalid redirect_uri` | Admin UI のオリジン(compose はホストポート **13000** で公開)を realm クライアントの `redirectUris`/`webOrigins` に登録する必要があります。同梱 dev realm は `http://localhost:13000` を登録済み。独自 realm やホストポート変更時は対応するエントリを追加してください。 |
| トークン取得に失敗 | Keycloak がまだ healthy でない。`docker compose ps` で確認し、起動後に再試行。 |
| `/telemetry` の `buffer_depth` が増え続ける | Building OS へのアップリンク断。フレームがバッファ中(`mock-bos` 再起動時など想定内)。 |
| ゲートウェイがコネクタを管理できない | コンテナに host Docker socket(`/var/run/docker.sock`)のマウントが必要。`docker-compose.yml` 参照。 |
