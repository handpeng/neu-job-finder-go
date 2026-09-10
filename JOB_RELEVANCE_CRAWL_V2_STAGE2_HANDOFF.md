# Job Relevance & Crawl Completeness V2 - Stage 2 Handoff

```text
NEU_JOB_FINDER_JOB_RELEVANCE_CRAWL_V2_STAGE2_HANDOFF
STATUS=IMPLEMENTED_PENDING_STAGE3
REPOSITORY=handpeng/neu-job-finder-go
PROGRAM=#1
METHOD_VERSION=2026-09-03-v3.1
STAGE2_CHAIN=#2->#3->#4->#5->#6->#7->#8->#9->#10
STAGE1_PLANNING_PR=#11
STAGE1_PLANNING_HEAD=8b7ab907935e8322e1c9bfb583d1f758fabbe0aa
STAGE1_PLANNING_MERGE_SHA=b136086c5070532b70e874958a8296eec7168b1c
STAGE2_START_BASELINE=b136086c5070532b70e874958a8296eec7168b1c
I1_PR=#12 REVIEWED_HEAD=f71c9aaa4c6e8a1d88028f7455b4a8342edd067a MERGE_SHA=140d9e957b875fb676a915865efbc43940dd8b57
I2_PR=#13 REVIEWED_HEAD=f66bd014b341eece7998b31b660d0a3fa1c21cc6 MERGE_SHA=08855360ac74f7e0b9b22c7c30ce10a8b4402644
I3_PR=#14 REVIEWED_HEAD=761f28915cd8608ebd4dfddd148a69b76839f4af MERGE_SHA=daf0fe43662149d342a73a27b3333ecde139d991
I4_PR=#15 REVIEWED_HEAD=abf571f3a46180ecf845a5aca22beef31d999731 MERGE_SHA=d8d4855fcf836bc86fc2e7dcb03cbf98ce8711e4
I5_PR=#16 REVIEWED_HEAD=84ce85a464cd87846686f0a4d0cbd6b2436163fa MERGE_SHA=d8546402eccfe182b4da856c4d8edfafec0eea9a
I6_PR=#17 REVIEWED_HEAD=cfa6c55f59656317b8a8cf74b891e329e8c12d0d MERGE_SHA=8a1293870b7e3190962db75eabc0a568605ffbf5
I7_PR=#18 REVIEWED_HEAD=a179c6bb32827a4c0c237b5bcd918f3145b5d585 MERGE_SHA=3248db3c3177b2000038e100a5baa014a7b6a65e
I8_PR=#19 REVIEWED_HEAD=5cac29ccc43586f850fbb562a4efa37b58d746b2 MERGE_SHA=78493f128290fae10a55f27cc9c281ae55d733a7
I9_PR=#20 REVIEWED_HEAD=70b85d42a8e52230462f50a6c74d0be34435adb3 MERGE_SHA=d3e64848eb933f288918e6227a80ee8eebe73482
FINAL_STAGE2_CANDIDATE_SHA=d3e64848eb933f288918e6227a80ee8eebe73482
FULL_REGRESSION=PASS
CI=PASS
WORKTREE_CLEAN=YES
ORIGIN_MAIN_MATCH=YES
PROGRAM_OPEN=YES
STAGE3_STARTED=NO
TAG_CREATED=NO
RELEASE_CREATED=NO
PACKAGE_PUBLISHED=NO
PRODUCTION_CUTOVER=NO
AUTO_APPLICATION_PERFORMED=NO
```

## Issue Evidence

### ISSUE=#2

```text
PR=#12
REVIEWED_HEAD_SHA=f71c9aaa4c6e8a1d88028f7455b4a8342edd067a
FILES_CHANGED=internal/matcher/matcher.go; internal/matcher/matcher_test.go; internal/model/model.go
FOCUSED_TESTS=PASS: go test ./internal/matcher
FULL_REGRESSION=PASS: go test ./...
RACE_TEST=NOT_APPLICABLE: matcher-only change; concurrency race gate exercised in #7 and final regression
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=140d9e957b875fb676a915865efbc43940dd8b57
KNOWN_LIMITATIONS=Position-local fragments and explicit provenance were added; richer local fragment extraction remained in parser hardening. Generic announcement fallback is explicit and confidence-capped.
```

### ISSUE=#3

```text
PR=#13
REVIEWED_HEAD_SHA=f66bd014b341eece7998b31b660d0a3fa1c21cc6
FILES_CHANGED=README.md; internal/model/model.go; internal/matcher/matcher.go; internal/matcher/matcher_test.go
FOCUSED_TESTS=PASS: go test ./internal/matcher
FULL_REGRESSION=PASS: go test ./...
RACE_TEST=NOT_APPLICABLE: Boolean matcher-only change; concurrency race gate exercised in #7 and final regression
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=08855360ac74f7e0b9b22c7c30ce10a8b4402644
KNOWN_LIMITATIONS=Boolean groups use the bounded JSON model: terms within a group are OR, MUST groups are AND, and SHOULD counts groups. UI/API wiring was completed in #10.
```

### ISSUE=#4

```text
PR=#14
REVIEWED_HEAD_SHA=761f28915cd8608ebd4dfddd148a69b76839f4af
FILES_CHANGED=README.md; internal/matcher/matcher.go; internal/matcher/matcher_test.go
FOCUSED_TESTS=PASS: go test ./internal/matcher
FULL_REGRESSION=PASS: go test ./...
RACE_TEST=NOT_APPLICABLE: ranking-only change; concurrency race gate exercised in #7 and final regression
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=daf0fe43662149d342a73a27b3333ecde139d991
KNOWN_LIMITATIONS=ResultOptions was initially exposed at the matcher layer; Web/API/export controls were unified in #10.
```

### ISSUE=#5

```text
PR=#15
REVIEWED_HEAD_SHA=abf571f3a46180ecf845a5aca22beef31d999731
FILES_CHANGED=README.md; cmd/sync/main.go; internal/crawler/crawler.go; internal/crawler/crawler_test.go; internal/webapp/webapp.go; internal/webapp/web/templates/index.html; internal/webapp/web/static/app.js
FOCUSED_TESTS=PASS: go test ./internal/crawler -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=d8d4855fcf836bc86fc2e7dcb03cbf98ce8711e4
KNOWN_LIMITATIONS=Undated list entries remain eligible and prevent date-based stopping; source keyword forwarding is limited to one verified keyword, with multi-keyword matching performed locally as OR.
```

### ISSUE=#6

```text
PR=#16
REVIEWED_HEAD_SHA=84ce85a464cd87846686f0a4d0cbd6b2436163fa
FILES_CHANGED=README.md; cmd/sync/main.go; internal/crawler/crawler.go; internal/crawler/crawler_test.go; internal/store/store_test.go; internal/webapp/webapp.go; internal/webapp/web/templates/index.html; internal/webapp/web/static/app.js
FOCUSED_TESTS=PASS: go test ./internal/crawler ./internal/store -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=d8546402eccfe182b4da856c4d8edfafec0eea9a
KNOWN_LIMITATIONS=The default seven-day policy treats older source-published announcements as stable indefinitely; recent or undated cached entries refresh after the LastSeenAt window, and force refresh remains explicit.
```

### ISSUE=#7

```text
PR=#17
REVIEWED_HEAD_SHA=cfa6c55f59656317b8a8cf74b891e329e8c12d0d
FILES_CHANGED=README.md; cmd/server/main.go; cmd/sync/main.go; internal/crawler/crawler.go; internal/crawler/crawler_test.go
FOCUSED_TESTS=PASS: go test ./internal/crawler -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=8a1293870b7e3190962db75eabc0a568605ffbf5
KNOWN_LIMITATIONS=Detail concurrency is bounded within one sync run by DetailWorkers and MaxConnections; one aggregate pacing gate covers list/detail/retry requests, and deterministic output is aggregated after fetch completion.
```

### ISSUE=#8

```text
PR=#18
REVIEWED_HEAD_SHA=a179c6bb32827a4c0c237b5bcd918f3145b5d585
FILES_CHANGED=README.md; cmd/sync/main.go; internal/crawler/crawler.go; internal/crawler/crawler_test.go; internal/model/model.go; internal/store/store.go; internal/store/store_test.go; internal/webapp/webapp.go; internal/webapp/webapp_test.go
FOCUSED_TESTS=PASS: go test ./internal/crawler ./internal/store ./internal/webapp -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=3248db3c3177b2000038e100a5baa014a7b6a65e
KNOWN_LIMITATIONS=Run evidence is stored in the local JSON ledger and exposed through crawler events/CLI output; cross-format result unification, full retry UI, and remote CI were completed in #10.
```

### ISSUE=#9

```text
PR=#19
REVIEWED_HEAD_SHA=5cac29ccc43586f850fbb562a4efa37b58d746b2
FILES_CHANGED=README.md; internal/crawler/crawler.go; internal/crawler/crawler_test.go; internal/matcher/matcher.go; internal/matcher/matcher_test.go; internal/model/model.go
FOCUSED_TESTS=PASS: go test ./internal/crawler ./internal/matcher -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=NOT_APPLICABLE_WITH_EXACT_REASON: GitHub Actions workflow was introduced in #10
POST_MERGE_MAIN_SHA=78493f128290fae10a55f27cc9c281ae55d733a7
KNOWN_LIMITATIONS=HTML parsing remains dependency-free bounded regex/structure handling; OCR/PDF/image attachment extraction remains out of scope. Ambiguous fields remain raw evidence but are excluded from verified matcher evidence.
```

### ISSUE=#10

```text
PR=#20
REVIEWED_HEAD_SHA=70b85d42a8e52230462f50a6c74d0be34435adb3
FILES_CHANGED=.github/workflows/ci.yml; README.md; cmd/sync/main.go; internal/export/export.go; internal/matcher/matcher.go; internal/matcher/matcher_test.go; internal/model/model.go; internal/webapp/web/static/app.js; internal/webapp/web/static/status.css; internal/webapp/web/templates/index.html; internal/webapp/webapp.go; internal/webapp/webapp_test.go; web/static/app.css; web/static/app.js; web/templates/index.html
FOCUSED_TESTS=PASS: go test ./internal/matcher ./internal/export ./internal/webapp -count=1
FULL_REGRESSION=PASS: go test ./... -count=1
RACE_TEST=PASS: go test -race ./... -count=1
CI=PASS: Go CI push run 34533072865 and pull_request run 34533074353; both verify jobs passed formatting, vet, test, and race steps
POST_MERGE_MAIN_SHA=d3e64848eb933f288918e6227a80ee8eebe73482
KNOWN_LIMITATIONS=No OCR/PDF/image extraction; source HTML parsing remains dependency-free and bounded; undated or invalid source dates remain eligible for result display; no automatic login or application action.
```

## Final Verification

The post-merge verification was run on local `main` with the dev-owned WSL Go toolchain at `/home/dev/.local/opt/go` (`go1.27.1`, because `/mnt/d/Go` was unavailable):

```text
gofmt verification: PASS (gofmt -l . produced no output)
go vet ./...: PASS
go test ./... -count=1: PASS
go test -race ./... -count=1: PASS
git diff --check: PASS
git rev-parse HEAD: d3e64848eb933f288918e6227a80ee8eebe73482 before this documentation-only handoff commit
git rev-parse origin/main: d3e64848eb933f288918e6227a80ee8eebe73482 before this documentation-only handoff commit
```

The final handoff document is evidence-only. Stage 3, tags, Releases, package publication, production cutover, recruitment-site login, and automatic application or job-submission actions were not performed.
