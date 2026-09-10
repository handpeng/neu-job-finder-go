# Job Relevance & Crawl Completeness V2 — Stage 1 Plan

METHOD_VERSION=2026-09-03-v3.1
PROGRAM=#1
STAGE1_BASELINE_SHA=5a7a1544dc16b7a5caac2c3a2f0e9457bafbf027
DEFAULT_BRANCH=main
PREDECESSOR_GATE=PASS

## Objective

Upgrade the initial NEU Job Finder into a position-level, explainable job retrieval and ranking system that avoids cross-position score contamination, supports explicit Boolean matching, returns the most relevant results first, and performs bounded, auditable, source-friendly incremental collection.

## Frozen problem statement

The current matcher may use announcement-wide text as positive semantic evidence for every position in the same announcement. In multi-position announcements, this can raise unrelated administrative, finance, sales, or other positions because another position contains relevant terms such as metallurgy, AI, machine learning, Python, or process modeling. Current matching is coverage averaging rather than explicit Boolean matching. Current result surfaces return all scored jobs without a first-class minimum-score/Top-N policy. Detail collection is serial and storage-level deduplication does not yet constitute true network incrementality.

## Frozen design principles

1. Correctness before throughput: eliminate position-level evidence contamination before changing ranking or concurrency.
2. Eligibility before score: Boolean/hard-condition eligibility is evaluated before weighted ranking.
3. Source evidence over crawler metadata: source publication date may rank freshness; LastSeenAt may not.
4. Top-N is a result operation, not a destructive crawl/storage operation.
5. One sync run at a time remains valid; bounded concurrency is allowed inside the run.
6. Aggregate source request pacing must remain bounded and source-friendly even with multiple workers.
7. Ambiguous parser output must be observable rather than silently shifted into the wrong field.
8. Stage 2 must stop at IMPLEMENTED_PENDING_STAGE3; no qualification/release/tag/publish/application actions.

## Frozen Stage 2 chain

### I1 — #2 Position-level evidence isolation

Eliminate cross-position semantic evidence leakage; add provenance and adversarial multi-position fixtures.

### I2 — #3 Boolean matcher

Add MUST / SHOULD / MUST_NOT / minimum_should_match semantics, including practical AND/OR behavior and backward-compatible simple-field mapping.

### I3 — #4 Ranking and result limits

Separate eligibility from ranking; add min_score + top_n; deterministic ranking by score, source publication date, then stable ID; add positive/negative controls.

### I4 — #5 Crawl range completeness

Add inclusive start/end dates, explicit crawler keyword semantics, safe page stopping, and reconcilable discovery counts. Use source `starttime`/`endtime`/`keyword` only when verified, while retaining local validation.

### I5 — #6 True network incrementality

Use cache-aware detail scheduling with a documented refresh policy and explicit force refresh. New IDs must always fetch; old stable cached IDs should not be needlessly refetched.

### I6 — #7 Bounded concurrent details

Add a bounded worker pool, centralized rate limiting, retry/backoff, deterministic aggregation, explicit connection bounds, and concurrency tests.

### I7 — #8 Batch persistence and crawl ledger

Avoid full JSON rewrite per streamed item; batch persistence and record auditable run evidence, failed IDs, and targeted retry state.

### I8 — #9 Parser hardening

Harden position parsing against missing/reordered/extra row metadata and surface extraction confidence/fallback/ambiguity. OCR/attachment extraction remains out of scope.

### I9 — #10 Interface/CI/integration/handoff

Unify Web/API/CSV/Markdown/XLSX semantics, expose Boolean/ranking/date/refresh controls, handle expired-result filtering without deleting source data, remove/guard duplicate Web assets, add CI/integration tests, update docs, and produce the exact Stage 2 handoff.

## Serial execution contract

Stage 2 MUST execute exactly in dependency order:

`#2 -> #3 -> #4 -> #5 -> #6 -> #7 -> #8 -> #9 -> #10`

For every issue:

1. Sync local `main` to `origin/main`; require a clean worktree.
2. Read Program #1, this Stage 1 plan, and the current issue.
3. Create one bounded issue branch from current verified `main`.
4. Implement only that issue's scope.
5. Run focused tests plus `go test ./...`; run `go test -race ./...` when relevant/supported.
6. Commit and push the exact implementation.
7. Open one PR linked to the issue.
8. Review the exact PR head and changed files. If defects are found, repair on the same branch and re-review the new exact head.
9. Require tests/CI PASS before merge.
10. Merge the reviewed exact head.
11. Fetch/verify `origin/main` includes the merge and rerun required post-merge tests.
12. Post exact evidence to the issue: reviewed head SHA, merge SHA, tests, known limitations.
13. Close the implementation issue as completed only after post-merge verification.
14. Only then continue to the next dependency.

Do not run Stage 3 per issue. Stage 3 is a single fresh qualification after the entire Stage 2 chain has completed.

## Program-level Stage 2 acceptance gates

- Cross-position contamination adversarial tests fail closed.
- A relevant technical position remains high while unrelated positions in the same announcement do not inherit its semantic evidence.
- Boolean truth-table tests cover MUST/SHOULD/MUST_NOT/minimum_should_match and term-order invariance.
- MUST_NOT cannot be compensated by unrelated positive terms.
- `min_score` and `top_n` are deterministic and consistent across user-facing result surfaces.
- Top-N/min-score never delete or skip source records merely because they rank low.
- Ranking freshness is based on `PublishedDate`, not `LastSeenAt`.
- Date interval is inclusive; invalid reversed intervals fail validation.
- Mixed/undated list pages cannot cause in-range IDs to be silently discarded.
- Re-running an unchanged range uses materially fewer detail requests under default incremental policy.
- New IDs always fetch; explicit force refresh remains available.
- Worker concurrency and connection counts are bounded; aggregate request pacing remains governed.
- Partial detail failures preserve successes and record exact failed IDs/reasons.
- Batch persistence prevents one full-store rewrite per item.
- Parser ambiguity is surfaced rather than silently misassigning semantic fields.
- Web/API/CSV/MD/XLSX return the same eligible ordered result set for identical controls.
- Expired filtering is explicit and non-destructive.
- CI and clean-checkout local regression pass on the final candidate.
- `origin/main` equals local `main`; worktree clean.

## Explicit non-goals

- Automatic login or job application/submission.
- Unbounded external-site crawling.
- OCR/image/PDF attachment extraction.
- LLM/embedding/reranker service dependencies.
- Distributed crawling.
- Unnecessary database migration.
- Aggressive source request rates.

## Required terminal Stage 2 handoff

The final integration issue (#10) must create `JOB_RELEVANCE_CRAWL_V2_STAGE2_HANDOFF.md` and report:

```text
NEU_JOB_FINDER_JOB_RELEVANCE_CRAWL_V2_STAGE2_HANDOFF
STATUS=IMPLEMENTED_PENDING_STAGE3
REPOSITORY=handpeng/neu-job-finder-go
PROGRAM=#1
METHOD_VERSION=2026-09-03-v3.1
STAGE2_CHAIN=#2->#3->#4->#5->#6->#7->#8->#9->#10
STAGE1_PLANNING_PR=<number>
STAGE1_PLANNING_MERGE_SHA=<sha>
I1_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I2_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I3_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I4_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I5_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I6_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I7_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I8_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
I9_PR=<number> REVIEWED_HEAD=<sha> MERGE_SHA=<sha>
FINAL_STAGE2_CANDIDATE_SHA=<exact origin/main sha>
FULL_REGRESSION=PASS
CI=PASS
WORKTREE_CLEAN=YES
ORIGIN_MAIN_MATCH=YES
STAGE3_STARTED=NO
TAG_CREATED=NO
RELEASE_CREATED=NO
PACKAGE_PUBLISHED=NO
AUTO_APPLICATION_PERFORMED=NO
```

Stop immediately after emitting/posting this Stage 2 handoff. Do not begin Stage 3.