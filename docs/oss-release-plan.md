# OSS 公開準備計画

作成日: 2026-06-20  
対象ブランチ: master

---

## 現状評価サマリー

現時点で「あって当然」の OSS 基盤は 7〜8 割できている。  
残りのギャップは主に **法的整備・コミュニティ運営インフラ・外部コントリビューター向けドキュメント** の 3 領域に集中している。

| 領域 | 状態 |
|------|------|
| ライセンス (Apache-2.0) | ✅ 完了 |
| SECURITY.md | ✅ 完了 |
| CONTRIBUTING.md (DCO・Conventional Commits) | ✅ 完了 |
| README.md / README.ja.md（日英二言語） | ✅ 完了 |
| CONTEXT.md（ドメイン用語集） | ✅ 完了 |
| Getting Started（日英） | ✅ 完了 |
| ADR × 7 本 | ✅ 完了 |
| GitHub Issue テンプレート | ✅ あり（要改善） |
| Makefile / CI（Go・E2E・Admin UI） | ✅ 完了 |
| **CODE_OF_CONDUCT.md** | ❌ 未作成 |
| **PR テンプレート** | ❌ 未作成 |
| **著作権ヘッダー（ソースファイル）** | ❌ 未付与 |
| **NOTICE ファイル** | ❌ 未作成 |
| **Dependabot** | ❌ 未設定 |
| **CODEOWNERS** | ❌ 未作成 |
| **CHANGELOG / リリースフロー** | ❌ 未整備 |
| **Connector SDK 仕様書** | ❌ 未作成 |
| **CI バッジ** | ❌ README に未追加 |
| Makefile の IP アドレスハードコード | ⚠️ 要置換 |
| GUTP / SBCO の外部向け説明 | ⚠️ 要補足 |
| Issue テンプレートの AI エージェント専用記述 | ⚠️ 要検討 |
| `docs/memory/` の公開可否 | ⚠️ 要判断 |
| バージョニング戦略 | ⚠️ 未定義 |

---

## Phase 1 — 法的整備（公開前・必須）

公開日までに必ず完了させる。法的リスクと外部コントリビューターへの信頼性に直結する。

### 1-1. CODE_OF_CONDUCT.md の作成

**作業**: Contributor Covenant v2.1 を日本語訳付きで配置。  
連絡先メールアドレス（行動規範違反の報告先）を決定して埋める。

```
# 新規作成
CODE_OF_CONDUCT.md
```

**判断が必要な事項**: 報告先メールアドレス（個人アカウントか、プロジェクト専用メールか）

---

### 1-2. ソースファイルへの著作権ヘッダー付与

Apache-2.0 の推奨形式。Go・Python・Java・TypeScript の各ファイルに付与する。

```go
// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
```

**対象ファイル数の目安**:
- `cmd/`, `internal/`, `connector/` 配下の `.go` (多数)
- `connector/bacnet/` 配下の `.py`
- `connector/opcua/` 配下の `.java`
- `admin-ui/` 配下の主要 `.ts` / `.tsx`

**作業効率化**: `addlicense` ツール（`golang.org/x/tools` 系）で一括付与できる。

```bash
go install github.com/google/addlicense@latest
addlicense -c "nexus-gateway contributors" -l apache -s=only ./...
```

---

### 1-3. NOTICE ファイルの作成

Apache-2.0 Section 4(d) に基づく attribution 告知。  
主要 OSS 依存ライブラリ（NATS、BACpypes3、Eclipse Milo 等）の帰属を記載。

```
# 新規作成
NOTICE
```

内容例:
```
nexus-gateway
Copyright 2026 nexus-gateway contributors

This product includes software developed at:
- NATS.io (https://nats.io) — Apache-2.0
- BACpypes3 (https://bacpypes3.readthedocs.io) — MIT
- Eclipse Milo (https://github.com/eclipse/milo) — EPL-2.0
...
```

**注意**: EPL-2.0 の Eclipse Milo は Apache-2.0 と組み合わせる際の互換性確認が必要（EPL-2.0 は "Secondary License" として Apache-2.0 との組み合わせを許可している）。

---

### 1-4. LICENSE の著作権表示確認

現状の `LICENSE` に `Copyright 2026 takashikasuya` とある。  
**判断が必要な事項**: 組織への移管か、個人名義のままか。  
選択肢:
- (a) `Copyright 2026 takashikasuya` のまま（個人名義 OSS）
- (b) `Copyright 2026 nexus-gateway contributors`（コミュニティ名義）
- (c) 組織（GitHub Org）を作成し、そちらに移管

---

## Phase 2 — コミュニティ運営インフラ（公開前・重要）

外部からのコントリビューションを受け入れる体制を整える。

### 2-1. PR テンプレートの作成

```
# 新規作成
.github/PULL_REQUEST_TEMPLATE.md
```

記載内容:
- 変更内容とその理由（Why）
- 関連する ADR / Epic / Issue 番号
- テスト確認方法
- ADR 違反がないことの確認チェックボックス
- DCO sign-off の確認（`git commit -s`）
- wire contract への影響有無（`proto/` を変更した場合は `make buf-breaking` 実行済みか）

---

### 2-2. Issue テンプレートの改善

現状の Issue テンプレートには「Notes for AI agent」セクションがある。  
外部コントリビューターから見ると異質に映るため、以下いずれかに対応する。

**選択肢**:
- (a) セクション名を「Context for implementers」に変更し、AI エージェント固有の記述を汎用化
- (b) 削除して CONTRIBUTING.md 参照に置き換え

また、Issue の種別として現状 `bug` / `feature` の 2 種類のみ。  
追加を検討:
- `question` — 使い方の質問
- `docs` — ドキュメントの改善提案
- `connector` — 新プロトコルコネクタの提案

---

### 2-3. CODEOWNERS の作成

```
# 新規作成
.github/CODEOWNERS
```

```
# Default owner for all files
*                   @takashikasuya

# gRPC contract changes require explicit review
/proto/             @takashikasuya

# Connector additions require protocol expertise review
/connector/bacnet/  @takashikasuya
/connector/opcua/   @takashikasuya
```

---

### 2-4. Dependabot の設定

自動セキュリティ更新。Go・npm・GitHub Actions の 3 エコシステムを対象とする。

```yaml
# 新規作成: .github/dependabot.yml
version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
  - package-ecosystem: "npm"
    directory: "/admin-ui"
    schedule:
      interval: "weekly"
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
```

---

### 2-5. CI バッジを README に追加

```markdown
[![CI](https://github.com/takashikasuya/nexus-gateway/actions/workflows/ci.yml/badge.svg)](...)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-blue.svg)](go.mod)
```

README.md および README.ja.md の両方に追加。

---

### 2-6. GitHub DCO ボットの設定

CONTRIBUTING.md では DCO sign-off を要求しているが、CI で強制されていない。  
GitHub App「DCO」を repository に有効化するか、ci.yml に DCO チェックを追加する。

**選択肢**:
- (a) [probot/dco](https://github.com/probot/dco) アプリを有効化（推奨）
- (b) `actions/checkout` + `git log` で sign-off を確認するステップを ci.yml に追加
- (c) 今はしない（コミュニティが小さいうちは人力確認でも可）

---

## Phase 3 — 外部コントリビューター向けドキュメント（公開後・優先）

外部の開発者がコネクタを書いたり、コアに貢献できるために必要。

### 3-1. Connector SDK 仕様書の作成

ADR-0006 では「外部ベンダがコネクタを配布・登録できる」と定義されているが、  
**具体的な仕様書が存在しない**。これがないと第三者はコネクタを書けない。

```
# 新規作成
docs/connector-sdk.md  (または docs/specs/connector-contract.md)
```

記載内容:
- Common Event のスキーマ（必須/任意フィールド、型、値域）
- NATS subject の規則（`evt.<protocol>.<connector_id>`）
- Control Command の受信方法（`cmd.<protocol>.<connector_id>`）
- Control Result のフォーマットと typed failure の種類
- Point List から何を読んでよいか（native address のみ）、何を読んではいけないか
- Dockerfile / OCI image の要件（ベースイメージ、ヘルスチェック）
- Connector Catalog manifest スキーマ
- `connector/{bacnet,opcua,mqtt}` をテンプレートとして使う手順
- conformance テストの実行方法（将来）

---

### 3-2. プロトコルバッファ API ドキュメント

`proto/gateway_ingress.proto` / `proto/gateway_egress.proto` は Building OS との  
契約だが、HTML または Markdown の生成ドキュメントがない。

```bash
# buf を使って protodoc 生成
buf generate --template buf.gen.doc.yaml
```

または protoc-gen-doc で `docs/proto/` に生成する。  
**特に外部コントリビューターがコネクタ以外（mock BOS, テストツール等）を作る際に必要。**

---

### 3-3. GUTP / SBCO の外部向け説明補足

README.md・README.ja.md の冒頭に、これらの用語の初出を説明する一文を追加する。

- **SBCO** (Smart Building Common Objects): SBCO データモデルビルダーのこと。  
  Point List のスキーマ定義元。
- **GUTP**: このプロジェクトが属する GitHub ユーザー/グループ名（`takashikasuya` の namespacing）。  
  外部向けには visible でないため、README では `gutp-building-os-oss` のリンクに説明を付ける。

---

### 3-4. Makefile のハードコード IP アドレスを置換

```makefile
# 現状（要変更）
OPCUA_ENDPOINT  ?= opc.tcp://192.0.2.10:4840
BACNET_ADDRESS  ?= 192.0.2.10

# 推奨（プレースホルダー化）
OPCUA_ENDPOINT  ?= opc.tcp://localhost:4840
BACNET_ADDRESS  ?= localhost
```

**理由**: 外部コントリビューターが clone した際に、ローカル開発者の LAN アドレスが混乱を招く。

---

## Phase 4 — リリースと継続運用（公開後・中期）

### 4-1. バージョニング戦略の決定

現状はバージョニングが存在しない（git タグなし）。  
OSS 公開時に最低限 v0.1.0 タグを打ち、セマンティックバージョニングを採用する。

**判断が必要な事項**:
- `v0.x.y`（pre-stable、破壊的変更あり）で始めるか
- proto 契約の breaking change ポリシーを明文化するか（CONTRIBUTING.md へ追記）

```bash
git tag v0.1.0 -a -m "Initial OSS release"
git push origin v0.1.0
```

---

### 4-2. CHANGELOG.md と GitHub Release の整備

```
# 新規作成
CHANGELOG.md  (Keep a Changelog 形式)
```

GitHub Release ワークフロー:

```yaml
# 新規作成: .github/workflows/release.yml
on:
  push:
    tags: ["v*"]
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
```

---

### 4-3. バックログ（docs/backlog/）の公開ロードマップ化

現状の `docs/backlog/epic/` は詳細だが、初めて来た人には全体感がつかみにくい。  
`docs/roadmap.md` に現在の優先度と状態を 1 ページで示す概要を追加する。

内容:
- MVP（達成済み項目）
- MVP+1（近期予定）
- 中期課題（BACnet/OPC-UA auto-discovery #90/#91、Connector SDK 公開等）
- スコープ外（明示）

---

## Phase 5 — 判断が必要な事項（オーナー決定待ち）

以下は技術的作業ではなく、オーナーが方針を決める必要がある事項。

| 事項 | 選択肢 | 推奨 |
|------|--------|------|
| **GitHub アカウント** | 個人 `takashikasuya` のまま / GitHub Organization を作成 | Organization 推奨（プロジェクト独立性・複数メンテナー対応） |
| **AGENTS.md の公開** | 公開のまま / 削除 / 改名 | 公開のまま（AI エージェント向け OSS 仕様として価値あり） |
| **`docs/memory/` の公開** | 公開のまま / private に / docs/ から除外 | 公開のまま（設計思想が透明で外部コントリビューターの助けになる） |
| **Issue テンプレートの AI 記述** | 削除 / 汎用化 | 汎用化（「Notes for AI agent」→「Context for implementers」） |
| **Connector Catalog の公開仕様化** | 先送り / v0.2 で対応 | v0.2 目標として Issue 登録を推奨 |
| **論文評価計画（evaluation-plan.ja.md）** | 公開のまま / README に文脈を明記 | 公開のまま。「学術評価目的」を README に一言追記 |

---

## 作業優先順と目安工数

```
Phase 1 — 法的整備          ████████░░  公開前・必須    推定 1〜2 日
  1-1  CODE_OF_CONDUCT      ░░░░  30 分（テンプレートから）
  1-2  著作権ヘッダー        ████  半日（addlicense ツール使用）
  1-3  NOTICE               ░░░░  1 時間
  1-4  LICENSE 著作権表示    ░░░░  30 分（方針決定次第）

Phase 2 — コミュニティ基盤  ██████░░░░  公開前・重要    推定 1 日
  2-1  PR テンプレート       ░░░░  1 時間
  2-2  Issue テンプレート改善 ░░░░  1 時間
  2-3  CODEOWNERS           ░░░░  30 分
  2-4  Dependabot           ░░░░  30 分
  2-5  CI バッジ             ░░░░  30 分
  2-6  DCO ボット            ░░░░  30 分

Phase 3 — 外部向けドキュメント ████████░░  公開後・優先   推定 2〜3 日
  3-1  Connector SDK 仕様書 ████  1〜2 日（最重要・最大工数）
  3-2  proto ドキュメント生成 ░░░░  2 時間
  3-3  GUTP/SBCO 説明補足   ░░░░  1 時間
  3-4  Makefile IP 置換     ░░░░  30 分

Phase 4 — リリース運用       ████░░░░░░  公開後・中期    推定 1 日
  4-1  v0.1.0 タグ打ち       ░░░░  30 分（方針決定次第）
  4-2  CHANGELOG + Release  ░░░░  2 時間
  4-3  公開ロードマップ       ░░░░  2 時間
```

---

## 最小公開セット（急ぐ場合）

以下 5 点だけ揃えれば、法的・コミュニティ最低基準は満たせる。

1. `CODE_OF_CONDUCT.md` — Contributor Covenant v2.1（30 分）
2. ソースファイルへの著作権ヘッダー — `addlicense` で一括（半日）
3. `NOTICE` ファイル — 主要依存ライブラリの帰属（1 時間）
4. `.github/PULL_REQUEST_TEMPLATE.md` — 最小限（1 時間）
5. Makefile の 192.0.2.10 をプレースホルダーに変更（30 分）

これで **公開ブロッカーはゼロ**になる。Connector SDK 仕様書は公開後の v0.2 で対応可。

---

## 参考: 既存ドキュメントで特に質の高いもの

以下はそのまま公開できる水準にある。変更不要。

- `CONTRIBUTING.md` — DCO、Conventional Commits、行動指針が明確
- `SECURITY.md` — GitHub private advisory の手順が明確
- `CONTEXT.md` — ドメイン用語集として完成度が高い
- `docs/background.ja.md` — 設計思想と類似システム比較が網羅的
- `docs/adr/` — 7 本の ADR が根拠とともに記録されている
- `AGENTS.md` — AI エージェント向けとして新しいが、設計意図が明確
