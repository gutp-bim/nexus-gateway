# 評価計画 — nexus-gateway

*[English](evaluation-plan.md) / 日本語* — トップ: [README](../README.ja.md)

本ドキュメントは nexus-gateway の実証評価に関する実験設計・指標定義・ワークロード表・
期待される知見をまとめたものです。E1〜E5 が論文必須評価、E6〜E7 は任意の拡張評価です。

対応するテストスキャフォールドは `test/e2e/eval_e*.go`（ビルドタグ `e2e`）にあります。

---

## セットアップ

### ソフトウェア構成

| コンポーネント | バージョン / イメージ |
|--------------|----------------------|
| nexus-gateway | 本リポジトリ (master) |
| NATS JetStream | `nats:2.10-alpine` |
| BACnet シミュレータ | `../bacnet-sim-gateway` |
| OPC-UA シミュレータ | `../opcua-sim-gateway` |
| Building OS モック | `../gutp-building-os-oss`（GatewayIngress gRPC スタブ） |
| Go | 1.25 |
| SQLite | `modernc.org/sqlite` 組み込み |

### ハードウェア（参考構成）

全評価を同一ホストで実施:

- CPU: 4 コア（Intel/AMD x86-64 または ARM64）
- RAM: 8 GiB
- ディスク: SSD、空き ≥10 GiB
- ネットワーク: ループバック（全コンポーネント同一ホスト）; WAN 遅延テストは `tc netem` を追加

### 各評価の環境変数

`test/e2e/eval_e*.go` の `Environment:` セクションに全変数を記載しています。
共通変数は下記のとおりです。

| 変数 | 既定値 | 用途 |
|------|--------|------|
| `E2E_NATS_URL` | —（必須） | NATS JetStream URL |
| `E2E_ADMIN_URL` | `http://localhost:18080` | nexus-gateway Admin API |
| `E2E_BOS_API_URL` | — | Building OS REST API（SoS テスト用） |

### 単一評価の実行

```bash
# 統合スタック起動（OPC-UA プロファイル）:
docker compose -f docker-compose.yml -f docker-compose.integration.yml \
  --profile opcua up -d

# E1 スループット（1000 ポイント、1 秒間隔）を単体実行:
E2E_NATS_URL=nats://localhost:14222 \
E2E_E1_POINTS=1000 \
E2E_E1_INTERVALS=1 \
E2E_E1_WINDOW=30 \
go test -v -tags e2e -run TestE1 ./test/e2e/

# 必須評価全件（フルマトリックスは約 3 時間）:
E2E_NATS_URL=nats://localhost:14222 \
go test -v -tags e2e -timeout 4h ./test/e2e/
```

---

## E1 — テレメトリスループットスケーリング

**仮説**: nexus-gateway はポイント数・ポーリング間隔のマトリックス全体にわたって
目標スループット（events/s）を維持し、NATS バックログの無制限積増しや SQLite リング
バッファの枯渇を起こさない。

### ワークロードマトリックス

| ポイント数 | 間隔 | 目標 events/s | 目標 frames/s |
|-----------|------|--------------|--------------|
| 100       | 1 s  | 100          | 100          |
| 100       | 10 s | 10           | 10           |
| 100       | 60 s | 1.7          | 1.7          |
| 1 000     | 1 s  | 1 000        | 1 000        |
| 1 000     | 10 s | 100          | 100          |
| 5 000     | 1 s  | 5 000        | 5 000        |
| 10 000    | 60 s | 167          | 167          |

### 測定指標

| 指標 | 単位 | 取得元 |
|------|------|--------|
| `events_per_s` | ev/s | JetStream メッセージ数 / ウィンドウ時間 |
| `frames_per_s` | fr/s | `storefwd_sent_total` デルタ / ウィンドウ時間 |
| `nats_lag` | メッセージ数 | ウィンドウ末のJetStream ストリーム状態 |
| `sqlite_depth` | 行数 | `/metrics` `storefwd_buffer_depth` |
| `cpu_delta_s` | s | `/metrics` `process_cpu_seconds_total` デルタ |
| `mem_mib` | MiB | `/metrics` `process_resident_memory_bytes` |

### 期待される知見

- events/s は理論値（ポイント数 ÷ 間隔）の ±5% 以内に収まる。
- NATS ラグは ≤1 000 ポイント/1 s でほぼゼロ、5 000+/1 s で上昇傾向。
- SQLite 深度は適度なレートでほぼゼロ、バックプレッシャー時に増加。
- CPU は 1 000 ポイント/1 s でコア1つ未満、スループットにほぼ比例してスケール。

### テストスキャフォールド

`test/e2e/eval_e1_throughput_test.go` — `TestE1_ThroughputScaling`

---

## E2 — エンド・ツー・エンドレイテンシ

**仮説**: パイプラインが加算するレイテンシはプロトコルポーリング周期に比べて無視でき、
支配的な寄与はコネクタのプロトコル読み取りインターバルである。

### ステージ別分解

```
機器読み取り → T0（Common Event の device timestamp）
NATS 発行    → T1（JetStream メッセージ到着）      ← コネクタステージ
Normalizer   → T2（TelemetryFrame 発行）           ← Normalizer ステージ
S&F 書込み   → T3（SQLite 行挿入）                 ← S&F ステージ
gRPC 送信    → T4（フレームをデキューして送信）      ← Uplink ステージ
BOS 受信     → T5（GatewayIngress.StreamTelemetry 受信）← ネットワーク/BOS
```

### 測定指標

| ステージ | プロキシ測定 | 単位 |
|---------|------------|------|
| T0→T1（機器→NATS） | JetStream 消費時刻 − `event.Timestamp` | ms |
| T1→T3（NATS→S&F） | 現状は直接観測不可；目標 < 50 ms | ms |
| T3→T4（S&F→gRPC） | 現状は直接観測不可；目標 < 100 ms | ms |
| T4→T5（gRPC→BOS） | モックイングレスの受信タイムスタンプ − 送信タイムスタンプ | ms |

各ステージで p50 / p95 / p99 / max を報告。

### 期待される知見

- p99 機器→NATS はポーリング間隔 + 200 ms 以内。
- 全体レイテンシ（T0→T5）p99 は通常負荷でポーリング間隔の 2 倍未満。
- 5 000+/1 s でのレイテンシ劣化は段階的（クラッシュではない）。

### テストスキャフォールド

`test/e2e/eval_e2_latency_test.go` — `TestE2_EndToEndLatency`

---

## E3 — Store-and-Forward 復旧

**仮説**: 有界 SQLite リングバッファは 10 分以内のアップリンク停止中もテレメトリを
無損失で保持し、長時間停止では最古エントリを制御された方法でドロップし、復旧後は
バッファされたデータを速やかにドレインする。

### アウテージシナリオ

| 停止時間 | ポイント数 | 間隔 | 予想バッファ数 | 予想ドロップ数 |
|---------|-----------|------|-------------|-------------|
| 1 分    | 100       | 1 s  | ≤ 6 000     | 0           |
| 10 分   | 100       | 1 s  | ≤ 60 000    | 0           |
| 30 分   | 100       | 1 s  | ≤ 180 000   | 0（容量 ≥ 180 000 の場合） |
| 60 分   | 100       | 1 s  | ≤ 360 000   | 容量依存    |

リングバッファ容量は `STOREFWD_MAX_ROWS` で設定（既定: 1 000 000 行）。

### 測定指標

| 指標 | 単位 | 取得元 |
|------|------|--------|
| `max_buffer_depth` | 行数 | アウテージ中の `/metrics` |
| `dropped_total` | 行数 | `/metrics` `storefwd_dropped_total` |
| `recovery_time_s` | s | アップリンク復旧 → depth=0 までの経過時間 |
| `sent_after_recovery` | フレーム | 復旧後の `storefwd_sent_total` デルタ |

### 期待される知見

- バッファ容量 / ポイントレート 以下の停止ではドロップゼロ。
- 復旧時間はバッファ行数とアップリンク帯域に比例。
- Drop-oldest セマンティクス: 復旧後は最新データが Building OS に先着。

### テストスキャフォールド

`test/e2e/eval_e3_storefwd_test.go` — `TestE3_StoreFwdRecovery`

---

## E4 — Point List ドリフトとマッピング更新

**仮説**: ゲートウェイは新しい Point List に 1 回のポーリング間隔（既定 10 分；
`/point-list-refresh` Admin API で強制可）以内に収束し、マッピング変更後も正確に
イベントを解決/再ルーティングする。

### サブシナリオ

| シナリオ | 入力 | 期待出力 |
|---------|------|---------|
| `unknown` | Point List にない `local_id` を送信 | `normalizer_unresolved_total` +1/イベント；TelemetryFrame なし |
| `remap`   | `local_id` を新しい `point_id` にマッピング変更 | 1 回のポーリング間隔内に同期；以降のフレームは新 `point_id` を使用 |
| `unit`    | 既存 `local_id` の単位変更 | 同期後のフレームは新しい正規単位を使用 |

### 測定指標

| 指標 | 単位 | 取得元 |
|------|------|--------|
| `unresolved_ratio` | 割合 | `normalizer_unresolved_total` / 総イベント数 |
| `sync_time_s` | s | リフレッシュトリガー → 新マッピングを持つ最初のフレーム |
| `accepted_after_remap` | フレーム | 同期後の `storefwd_sent_total` デルタ |

### 期待される知見

- `unknown` 比率は Point List に存在しないポイントの割合に等しい。
- `remap` の同期時間は Admin API 強制時 < 5 s、ポーリング時 < ポーリング間隔。
- `unit` 変更は `remap` と同じタイミングで反映される。

### テストスキャフォールド

`test/e2e/eval_e4_pointlist_drift_test.go` — `TestE4_PointListDrift`

---

## E5 — 制御コマンド安全性

**仮説**: real-time-or-fail 制御パス（ADR-0004）は、期限切れ・重複・型エラーの
コマンドを物理設備に送信せず、アップリンク停止時はバッファせずに即座に失敗する。

### サブシナリオ

| シナリオ | 刺激 | 期待される応答 |
|---------|------|-------------|
| `stale_deadline` | 過去の `expired_at` を持つコマンド | 同期エラー応答；書込みなし |
| `typed_failure`  | 型違いの値（float 期待に string） | タイムアウト内の型エラー応答 |
| `idempotent`     | 同じ `control_id` を 2 回送信 | 2 回目の応答 = キャッシュされた 1 回目；二重書込みなし |
| `no_buffer`      | アップリンク停止中にコマンド送信 | 即座の失敗（NATS タイムアウト）；キューへの積み上げなし |

### 測定指標

| 指標 | 単位 |
|------|------|
| `passed` | シナリオごとの bool |
| `latency_ms` | 応答レイテンシ（ms） |

### 期待される知見

- 4 シナリオすべて合格。
- 期限切れ・型エラー応答は NATS request-reply タイムアウト（< 10 s）内に到着。
- 期限切れ・型エラー・アップリンク停止の各シナリオでは設備への書込みが起きない。

### テストスキャフォールド

`test/e2e/eval_e5_control_safety_test.go` — `TestE5_ControlCommandSafety`

---

## E6 — コネクタ更新とロールバック（任意）

**仮説**: Connector Catalog 経由のコネクタアップグレード（cosign 署名済み OCI、
digest 固定、stop→replace→health-check→rollback）はテレメトリギャップが 2 回の
ポーリング間隔以内で完了し、health-check 失敗時は自動ロールバックする。

### 測定指標

| 指標 | 単位 | 取得元 |
|------|------|--------|
| `detection_time_s` | s | カタログポーリング → Admin API が新イメージ Pending を確認 |
| `pull_verify_time_s` | s | 検出 → cosign 検証 OK + イメージプル完了 |
| `downtime_s` | s | コネクタ停止 → health-check グリーン |
| `telemetry_gap_s` | s | 停止前の最後のイベント → 再起動後の最初のイベント |
| `rollback_triggered` | bool | health-check 失敗 → 旧イメージ再起動 |

### テストスキャフォールド

`test/e2e/eval_e6_connector_lifecycle_test.go` — `TestE6_ConnectorUpdateRollback`

**前提**: cosign 署名済みイメージを GHCR にプッシュし、Connector Catalog に登録済みであること。

---

## E7 — 隣接システムとの比較（任意）

この評価は定性的分析です。論文では nexus-gateway を以下と比較します:
EdgeX Foundry、ThingsBoard IoT Gateway、EMQX Neuron、Azure IoT Edge、Eclipse Kura。

| 比較軸 | nexus-gateway | 他システム |
|--------|--------------|-----------|
| レジストリ所有 | Building OS Digital Twin のみ | 独自レジストリあり（EdgeX Core Metadata 等） |
| 制御経路 | gRPC GatewayEgress → core NATS request-reply | 方式様々（REST、MQTT 等） |
| コネクタ分離 | プロトコルごとの OCI コンテナ | 方式様々 |
| Point List 正本 | Building OS（差分同期コピーをエッジに保持） | ローカルレジストリ |
| ロックインベクタ | Connector Catalog / gRPC 契約 | クラウド制御プレーンまたはプラットフォーム |

自動テストなし — [docs/background.ja.md](background.ja.md) に記載の公開文書とプロトタイプ比較による評価。

---

## 出力形式

各評価テストは `t.Log` に CSV ブロックを出力します。以下で抽出できます:

```bash
go test -v -tags e2e -run TestE1 ./test/e2e/ 2>&1 \
  | grep -A 9999 'E1 results (CSV'
```

CSV を論文の表に直接ペーストしてください（Numbers / Excel / LaTeX `pgfplotstable`）。

---

## 論文投稿情報

投稿先: 建築情報学会（Architectural Informatics Society of Japan）
投稿締切: TBD

評価と論文セクションの対応:

| 評価 | 論文セクション |
|------|-------------|
| E1   | §4.1 スループット |
| E2   | §4.2 レイテンシ |
| E3   | §4.3 耐障害性 |
| E4   | §4.4 セマンティック収束 |
| E5   | §4.5 制御安全性 |
| E6   | §4.6 運用ライフサイクル（任意） |
| E7   | §5 比較考察 |
