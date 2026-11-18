# Evidências da validação local do reaper do EventStore

Executado em 18/11/2026 num contêiner descartável: go version go1.25.0 linux/amd64, MongoDB 7 (replica set de 1 nó), PostgreSQL 16 e Node v22.23.5. Somente dados fictícios. Nenhuma conexão com produção.

Legado de referência: fono-inova-crm-back@10d7e75a. Somas SHA-256 dos arquivos copiados em `legacy-oracle/eventreaper/LEGACY_SHA256SUMS`; o oráculo confere essas somas antes de rodar.

## Testes do reaper (unitários, integração Mongo, oráculo)

```
--- PASS: TestDecideCharacterisesLegacyRule (0.00s)
--- PASS: TestRecoveryMessageIsTheLegacyText (0.00s)
--- PASS: TestConfigValidate (0.00s)
--- PASS: TestKnownDefectL1AgeComesFromCreatedAtNotFromProcessingStart (0.00s)
--- PASS: TestKnownDefectL2ReaperNeverIncrementsAttempts (0.00s)
--- PASS: TestShadowNeverWrites (0.00s)
--- PASS: TestShadowVerificationComparesPredictionsWithTheNextCycle (0.00s)
--- PASS: TestExecutorAppliesAndContinuesAfterAPerEventError (0.00s)
--- PASS: TestExecutorWithoutLeadershipDoesNothing (0.00s)
--- PASS: TestExecutorStopsTheCycleWhenTheLeaseIsLost (0.00s)
--- PASS: TestBatchLimitAndOrderFollowTheLegacyQuery (0.00s)
--- PASS: TestQueryFailureIsReportedAndTheNextCycleRecovers (0.00s)
--- PASS: TestOverlappingCyclesAreSkipped (0.00s)
--- PASS: TestLoopRunsAfterWarmupAndStopsOnCancel (0.01s)
--- PASS: TestParseMode (0.00s)
PASS
ok  	kas-core-pilot/internal/eventreaper	0.015s
    oracle_test.go:165: 10000 events, 18 cycles: 879 reset, 746 failed, 0 divergences
--- PASS: TestGoPortMatchesTheUnmodifiedNodeReaper (11.00s)
--- PASS: TestCandidatesUseTheLegacySelection (0.12s)
--- PASS: TestTheKASNeverRequestsPayloadOrMetadata (0.12s)
--- PASS: TestApplyWritesExactlyWhatTheLegacyReaperStores (0.12s)
--- PASS: TestCompletionBetweenReadAndWriteIsNotOverwritten (0.13s)
--- PASS: TestAttemptsChangedAfterReadMakesTheWriteMiss (0.13s)
--- PASS: TestAttemptsGuardDistinguishesNullAndMissing (0.12s)
--- PASS: TestTwoUnfencedExecutorsNeverMoveAnEventTwice (2.71s)
--- PASS: TestTwoLeasedExecutorsShareTheWorkWithoutOverlap (2.55s)
--- PASS: TestOnlyOneLiveLeaseHolder (0.13s)
--- PASS: TestFormerLeaseHolderIsFencedOut (0.53s)
--- PASS: TestRestartMidBatchConverges (1.26s)
--- PASS: TestUnreachableDatabaseIsReportedNotFatal (0.60s)
--- PASS: TestFencedClaimProtocolStopsAStaleConsumer (0.13s)
PASS
ok  	kas-core-pilot/internal/legacy/crmeventstore	19.663s
--- PASS: TestExecutorRefusesARemoteHost (0.00s)
--- PASS: TestExecutorRequiresTheDatabaseConfirmation (0.00s)
--- PASS: TestURIComesOnlyFromTheEnvironment (0.00s)
--- PASS: TestRejectsBadModeAndConfig (0.00s)
--- PASS: TestLoopbackDetection (0.00s)
PASS
ok  	kas-core-pilot/cmd/kas-event-reaper	0.005s
```

## Suíte completa, análise estática e detector de corrida

```
$ go test ./...
ok  	kas-core-pilot/cmd/kas-core	0.014s
ok  	kas-core-pilot/cmd/kas-event-reaper	0.006s
ok  	kas-core-pilot/internal/audit	1.275s
ok  	kas-core-pilot/internal/configstore	0.017s
ok  	kas-core-pilot/internal/eventreaper	0.018s
ok  	kas-core-pilot/internal/events	2.171s
ok  	kas-core-pilot/internal/httpapi	0.167s
ok  	kas-core-pilot/internal/legacy/crmeventstore	19.784s
ok  	kas-core-pilot/internal/metrics	0.745s
?   	kas-core-pilot/internal/mongotest	[no test files]
ok  	kas-core-pilot/internal/pgdb	0.012s
ok  	kas-core-pilot/internal/pgmigrate	0.108s
?   	kas-core-pilot/internal/pgtest	[no test files]
?   	kas-core-pilot/internal/strictjson	[no test files]

$ go vet ./...  (sem saída = sem achados)
$ staticcheck ./...  (2025.1.1; sem saída = sem achados)

$ go test -race ./...
ok  	kas-core-pilot/cmd/kas-core	1.027s
ok  	kas-core-pilot/cmd/kas-event-reaper	1.016s
ok  	kas-core-pilot/internal/audit	2.448s
ok  	kas-core-pilot/internal/configstore	1.077s
ok  	kas-core-pilot/internal/eventreaper	1.038s
ok  	kas-core-pilot/internal/events	4.048s
ok  	kas-core-pilot/internal/httpapi	1.290s
ok  	kas-core-pilot/internal/legacy/crmeventstore	28.192s
ok  	kas-core-pilot/internal/metrics	2.149s
?   	kas-core-pilot/internal/mongotest	[no test files]
ok  	kas-core-pilot/internal/pgdb	1.018s
ok  	kas-core-pilot/internal/pgmigrate	1.170s
?   	kas-core-pilot/internal/pgtest	[no test files]
?   	kas-core-pilot/internal/strictjson	[no test files]
```

## node-integration (regressão existente)

```
# tests 1
# suites 0
# pass 1
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 2627.55861
```

## Ponta a ponta: scripts/validate-event-reaper.sh

```

== build

== disposable MongoDB 7 (single-node replica set) on 127.0.0.1:27117

== seed 3000 fictitious events
eligible before: 2314

== 1. shadow mode writes nothing
{"level":"INFO","msg":"event_reaper_cycle","mode":"shadow","total":100,"reset":55,"failed":45,"lost_race":0,"errors":0,"duration_ms":7}
OK: database hash unchanged (a0176c73a4bd587b1a0db2ea82894d12)
OK: no payload in the report

== 2. executor refuses without the database confirmation
OK: refused

== 3. two executors at once, database paused 8 s, then the leader is killed (kill -9)
pausing MongoDB for 8 s
MongoDB back; eligible now: 2214
leader is exec-A; kill -9 (crash, lease not released). eligible now: 2114
new leader: exec-B
OK: no eligible event left
applied=2314 expected=2314 duplicates=0 cycles_with_db_error=4 skipped={'lease_error': 4, 'not_leader': 13} applied_by={'A': 200, 'B': 2114}
OK: every eligible event applied exactly once; pause reported and survived; leadership moved after the crash

== 4. final states match the legacy rule
{
  pending_from_reaper: 1285,
  failed_from_reaper: 1029,
  wrong_pending: 0,
  wrong_failed: 0,
  young_still_processing: 386,
  processed_untouched: 300,
  lease: {
    _id: 'crm-eventstore-reaper',
    holder: 'exec-B'
  }
}

validation finished
```
