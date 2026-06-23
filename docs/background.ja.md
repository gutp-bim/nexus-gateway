# 背景・位置づけ・技術課題

*[English](background.md) / 日本語* — トップ: [README](../README.ja.md)

このドキュメントは、nexus-gateway が「なぜ存在するのか」「何に対してどう位置づくのか」
「どんな技術課題を抱えるのか」を、設計判断(ADR)と外部システムとの比較に基づいて
整理したものです。実装の手引きは [README](../README.ja.md)、個別の決定は
[ADR](adr/)、用語は [CONTEXT.md](../CONTEXT.md) を参照してください。

---

## 1. 結論(位置づけ)

nexus-gateway は、単なる「BACnet / OPC-UA / MQTT ゲートウェイ」ではなく、**Building OS
を System of Record とするための、エッジ側プロトコル吸収層**として位置づけるのが適切
です。

Building OS が機器・ポイント・デジタルツインの正本を持ち、nexus-gateway は「接続と変換」
だけを担います。本システムの本質は、**ビル設備プロトコルの多様性を、Building OS の単一
の telemetry/control 契約 `(gateway_id, point_id)` へ収束させること**です。

特に重要なのは、EdgeX Foundry のような汎用 IoT エッジ基盤をそのまま採用しない理由です。
EdgeX の Core Metadata / Core Command は、Building OS がすでに持つ Digital Twin・機器
レジストリ・制御経路と重複します。そのため nexus-gateway は「軽量な Azure IoT Edge 風の
コンテナ型コネクタ管理 + gRPC uplink」に寄せています。

---

## 2. なぜこのシステムを作る必要があるのか

近年のスマートビルディングでは、空調・照明・電力・環境センサー・入退室・ロボット・外部
IoT デバイスなど、多様な設備データを横断的に扱う必要が高まっています。しかし現場の設備は、
BACnet・OPC-UA・Modbus・MQTT などのプロトコル、機器ベンダ固有のアドレス体系、BMS ごとの
命名規則に強く依存しており、上位アプリケーションが直接これらを扱うと、**建物ごと・ベンダ
ごとの個別実装**が避けられません。

### 2.1 プロトコル・アドレス体系・意味体系の分断

ビル設備には複数プロトコルが混在し、それぞれが独自のアドレッシングとセマンティクスを
持ちます。Building OS 側は `(gateway_id, point_id)` をキーにした単一の telemetry/control
契約を要求するため、エッジ側でプロトコル差異を吸収する層が必要になります。

この課題は一般的な IoT 分野にも存在します。W3C WoT も、既存標準を置き換えるのではなく
相互運用性を高めるために補完する設計思想を採っています。ただし nexus-gateway は WoT
ランタイムそのものではなく、実運用上の中心契約は Building OS の Point List / Digital
Twin / gRPC 契約です(WoT 的な抽象化思想は近いが、目的が異なります)。

### 2.2 ゲートウェイがレジストリを持ってはいけない

Building OS が System of Record、Digital Twin が機器・ポイント・メタデータ・制御権限の
所有者です。ゲートウェイは gRPC で接続し、ワイヤ上では `(gateway_id, point_id, value,
timestamp)` だけを送ります。

ゲートウェイが独自にデバイス/ポイントレジストリを持つと、Building OS 側ツインとの**二重
管理**になり、ポイント名・単位・制御可否・空間紐づけ・権限の不整合を生みます。したがって
nexus-gateway は「現場プロトコルを理解するが、ビルの意味論の正本にはならない」ことを
徹底します。

### 2.3 ベンダロックの回避 — 「製品」ではなく「契約」と「コネクタ」を分離する

Azure IoT Edge はモジュールを Docker 互換コンテナとしてエッジで実行し、ランタイムが
install / update / monitoring / communication を管理します。これは nexus-gateway の
「署名済み OCI コネクタを配布・更新する」設計に近い思想です。

ただし Azure IoT Edge をそのまま使うと Azure IoT Hub 中心のクラウド管理プレーンに寄り
ます。nexus-gateway は Connector Catalog・OCI image・cosign・Docker Engine API・gRPC・
NATS を用い、特定クラウドに依存しない構成を狙います。コネクタ配布も GHCR を MVP とし
つつ、Harbor / ECR / ACR / Artifact Registry / 閉域サイトへ展開可能とします。

---

## 3. 類似システムとの比較

| システム | 概要 | nexus-gateway との違い |
|----------|------|------------------------|
| **EdgeX Foundry** | Device Service・Core Metadata・Core Command・App Service・Security を持つ汎用 IoT edge platform | 機能は豊富だが、Building OS が既に持つ Digital Twin / Command 経路と重複。Device Service 的な分離思想だけ借り、レジストリやコマンド中枢は持たない |
| **Azure IoT Edge** | Docker 互換コンテナの module を edge で実行し、runtime が install/update/monitoring を担う | コンテナ型モジュール運用思想は近い。ただし Azure 管理プレーンでなく Building OS + Connector Catalog + OCI registry で独立運用 |
| **Eclipse Kura** | Java/OSGi ベースの OSS IoT Edge framework。デバイス管理・産業プロトコル対応 | OSGi 型ランタイム一体型。独立コンテナごとのコネクタや Building OS Point List との直接整合とは異なる |
| **Eclipse Hono** | 多数 IoT デバイスを protocol-neutral な backend API に接続(HTTP/MQTT/AMQP/CoAP) | 大規模接続基盤としては近いが、ビル設備 Point List・REC/Brick/QUDT・ネイティブアドレス解決が中心ではない |
| **ThingsBoard IoT Gateway** | ThingsBoard に legacy protocol を接続する OSS gateway(MQTT/Modbus/OPC-UA/BACnet) | プロトコル接続は近いが上位が ThingsBoard 前提。本 GW は Building OS の Twin/Point List を正本にする |
| **EMQX Neuron** | 産業プロトコルを MQTT 等へ変換する軽量 IIoT connectivity server | MQTT 化に強いが、本 GW の主目的は MQTT 化でなく canonical telemetry/control への正規化 |
| **OpenRemote** | 100% OSS の IoT device management platform(building management 対象) | プラットフォーム全体を提供する方向。本 GW は Building OS の下位に置く edge integration layer に責務限定 |

比較すると、nexus-gateway の独自性は「**汎用 IoT platform をもう一つ作ること**」ではなく、
「**Building OS / SBCO データモデルと現場プロトコルの境界を、最小責務のエッジ層として
実装すること**」にあります。

---

## 4. Building OS / SBCO との関係

### 4.1 Building OS との関係

Building OS(OSS Edition)は、MQTT / NATS 経由で HVAC・電力・環境センサー等を収集し、
OxiGraph による Digital Twin、Keycloak 認証、OpenTelemetry、NATS JetStream などで構成
される OSS スマートビルディング基盤です。これに対し nexus-gateway は次を担います。

1. フィールド設備と通信する
2. ネイティブアドレスを Common Event として出す
3. Point List により `local_id → point_id` を解決する
4. `(gateway_id, point_id, value, timestamp)` の TelemetryFrame に正規化する
5. gRPC で Building OS に送信する
6. Building OS からの制御指令を、該当プロトコルの write に変換する

(`Connectors → NATS JetStream → Normalizer → Store-and-Forward → gRPC → Building OS`)

### 4.2 SBCO / Point List との関係 — 責務分担

| レイヤー | 責務 |
|----------|------|
| **SBCO / smartbuilding_datamodel_builder** | Point List / データモデルの作成・編集・標準化。ポイント、機器、空間、単位、制御可否、ネイティブアドレスを定義 |
| **Building OS** | Digital Twin と Point List の正本。`point_id` の所有、認可、履歴保存、API、UI、分析基盤 |
| **nexus-gateway** | 現場接続と変換。Point List を同期し、ネイティブアドレスを canonical `point_id` へ変換。**Point List は編集しない** |

Point List の正本は **Building OS twin(OxiGraph `sbco:PointExt`)** にあり、ゲートウェイは
version token を見て snapshot を取得し差分同期するだけです([ADR-0003](adr/0003-point-list-source-of-truth.md))。
この分離は、ベンダ機器や現場プロトコルが変わっても上位の Building OS が同じ `point_id`
契約で動き続けるための境界です。

---

## 5. 技術的な挑戦

### 5.1 ID 解決をどこに置くか

最大の設計論点は、`local_id → point_id` の解決を**コネクタ側に持たせない**ことです
([ADR-0001](adr/0001-telemetry-pipeline-shape.md))。コネクタは `local_id` とネイティブ
device ref だけを持つ Common Event を出し、Normalizer が Point List と join して `point_id`
を解決します。これによりコネクタは Building OS のドメインモデルから独立し、BACnet コネクタ
は BACnet だけ、OPC-UA コネクタは OPC-UA だけを知ればよく、REC/Brick/QUDT の意味論は
Normalizer に閉じ込められます。

### 5.2 JetStream を Normalizer の前段に置く理由

Point List の `local_id → point_id` 対応や単位が後で変わった場合、正規化済み telemetry
しか残っていないと再解釈できません。raw Common Event を JetStream に保持しておけば、Point
List 修正後に**再 Normalization(replay)**できます([ADR-0005](adr/0005-jetstream-topology-bounded-replay.md))。
ビル設備のポイントリストは初期投入時に必ず揺れる(点名・単位・BACnet instance・OPC-UA
NodeId・設置場所・制御可否が後で修正される)ため、replay window は実運用上の安全弁です。

### 5.3 exactly-once を捨て、best-effort に寄せる判断

Telemetry delivery は best-effort で、厳密順序や exactly-once は目標にしません。SQLite の
有界リングバッファを使い、満杯時は古い reading を捨てます([ADR-0002](adr/0002-best-effort-store-and-forward.md))。
ビル設備 telemetry は秒〜分粒度の時系列であり、古い値の完全保存より最新状態・障害復帰・
運用継続性が重要です。exactly-once を狙うと frame id・unique 制約・重複排除まで巻き込み、
MVP の複雑度が大きく上がります。

### 5.4 制御系は「保存しない」ことが安全

Telemetry は buffer してよい一方、**Control Command は永続化しません**。制御は
real-time-or-fail とし、古い制御指令が障害復旧後に物理設備へ適用される危険を避けるため、
command は JetStream に載せず core NATS request-reply で期限付き実行します
([ADR-0004](adr/0004-control-path-reliable-within-window.md))。空調・照明・バルブ・発停
の制御は遅延適用されると安全・快適性・設備保護上の問題になるため、「telemetry は
best-effort buffer、control は stale write 禁止」という**非対称設計**が正しいのです。

### 5.5 コネクタ配布・更新のセキュリティ

コネクタは署名済み OCI image とし、digest pinning・cosign 検証・registry allowlist・
SBOM/脆弱性スキャン前提・stop → replace → health check → rollback を採用します
([ADR-0006](adr/0006-connector-distribution-signed-oci.md))。コネクタは物理設備に接続する
ため、任意コンテナを実行できると設備制御権限そのものが侵害されます。タグでなく digest
固定、署名必須、Catalog 掲載物だけ実行、という方針はサプライチェーンセキュリティ上も妥当
です。

### 5.6 mTLS と gateway identity

gateway ↔ Building OS の gRPC は Building OS の Traefik edge で mTLS 終端し、`gateway_id` を
クライアント証明書の CN/SAN に紐づけます。gRPC サービス自体は h2c で、証明書検証は edge
proxy に委譲されます([ADR-0007](adr/0007-transport-security-mtls-at-edge.md))。Keycloak/
OIDC は人間向け Admin API に限定し、機械間通信は mTLS で認証する分離です。長期稼働・無人
運用・閉域網・証明書ローテーションが前提のビル設備ゲートウェイでは、OIDC bearer token
より mTLS が自然です。

---

## 6. ベンダロック排除の観点

nexus-gateway がベンダロックを避ける有効な点:

1. **プロトコル処理を独立コネクタに閉じ込める** — BACnet=BACpypes3、OPC-UA=Eclipse Milo、
   MQTT=Eclipse Paho。各コネクタは Building OS ドメインモデルを持たない独立コンテナ。
2. **コネクタ追加仕様が明確** — Point List からネイティブアドレスだけを読み、
   `evt.<protocol>.<connector_id>` に Common Event を出し、`cmd.<protocol>.<connector_id>`
   で制御を受ける契約。
3. **標準的・置換可能な技術** — OCI image、Docker Engine API、cosign、NATS、gRPC、mTLS、
   ベンダ中立な OPC UA / BACnet。

### 注意すべき「新たなロックイン」

クラウドベンダロックは避けられても、**プロジェクト固有 API ロック**が生じ得ます。Building
OS の `gatewaybridge` gRPC 契約、Point List provisioning API、Connector Catalog manifest
schema が十分に公開・安定化されないと、外部ベンダがコネクタを作る際に Building OS 実装へ
強く依存します。これを避けるには:

1. Connector SDK / manifest schema / Common Event schema を**公開仕様**として固定する
2. Point List schema を SBCO として安定化し、バージョニングする
3. コネクタ **conformance test** を用意する
4. BACnet / OPC-UA / MQTT simulator による E2E 検証を標準化する
5. Building OS gRPC 契約の**互換性ポリシー**を明文化する
6. Connector Catalog を単一運営主体のブラックボックスにしない

---

## 7. 今後の重点課題

| 優先度 | 課題 | 理由 |
|--------|------|------|
| **高** | Point List provisioning API の確定 | [ADR-0003](adr/0003-point-list-source-of-truth.md) でも新 API が必要。これがないと Building OS を正本にできない(Building OS #224) |
| **高** | Connector contract の仕様化 | 外部ベンダ・第三者がコネクタを書けるかがロックイン排除の核心 |
| **高** | mTLS edge topology の実装確認 | [ADR-0007](adr/0007-transport-security-mtls-at-edge.md) で Building OS 側 Traefik / cert-manager / CN-SAN binding が外部依存として明記(Building OS #161) |
| 中 | conformance test / simulator E2E | 隣接 simulator repo + Building OS mock でコネクタ互換性を検証([fixtures/integration](../fixtures/integration/README.md)、`test/e2e`) |
| 中 | 運用 UI の権限・監査ログ | Admin UI は connector lifecycle を扱うため、操作監査・RBAC・rollback 履歴が必要 |
| 中 | Catalog governance | Connector Catalog が新たな中央集権・ロックイン点にならないよう manifest schema と署名ポリシーを公開 |
| 低〜中 | WoT TD / JSON-LD 連携 | Runtime 契約は Building OS Point List でよいが、外部公開・相互運用説明として WoT TD 生成は有用 |

---

## 8. 要約

nexus-gateway は、スマートビルにおける設備プロトコルの分断を、Building OS の共通データ
モデルへ接続するための**エッジ統合ゲートウェイ**です。プロトコル別コネクタ、Normalizer、
Store-and-Forward、gRPC uplink、Control dispatch を組み合わせ、現場設備と Building OS の
境界を明確化します。

設計上の特徴は、Building OS を System of Record とし、ゲートウェイを接続・変換に限定する
点です。Point List の正本は Building OS twin にあり、ゲートウェイは差分同期した copy で
`local_id → point_id` を解決します。これにより第二の機器レジストリやコマンドサービスを
作らず、データモデル・権限・制御契約の一貫性を Building OS 側に集約します。

EdgeX Foundry・Azure IoT Edge・Eclipse Kura・Eclipse Hono・ThingsBoard・EMQX Neuron・
OpenRemote のような汎用 IoT platform ではなく、Building OS / SBCO データモデルに特化した
軽量なプロトコル吸収層です。EdgeX の Device Service 的分離や Azure IoT Edge 的 container
module lifecycle は参考にしつつ、Core Metadata / Core Command のような上位責務は Building
OS に残します。

技術的挑戦は、ID 解決、replay 可能な raw event 保持、best-effort telemetry、stale command
禁止、署名済み OCI connector 配布、mTLS による gateway identity、Point List provisioning
API の安定化にあります。とりわけ、telemetry は欠損を許容する一方 control は永続化せず
real-time-or-fail とする**非対称設計**は、ビル制御システムとして妥当です。

ベンダロック排除には OSS ライブラリ・標準プロトコル・OCI image・gRPC・NATS・mTLS・共通
Point List が有効ですが、Connector Catalog・provisioning API・gatewaybridge gRPC contract
が閉じた仕様のままだと、クラウドロックインの代わりに**プロジェクト固有 API ロック**が生じ
ます。今後は Connector SDK・各種 schema・conformance test を公開仕様として整備することが
重要です。
