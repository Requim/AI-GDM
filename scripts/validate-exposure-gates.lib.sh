#!/usr/bin/env sh

POSTGRES_TEST_NAMES="TestExposureOverpassLimitationSurvivesPostgresLossHTTPChain \
TestExposureRejectsMissingOverpassCoordinateBeforePostgresProjection \
TestExposureRejectsBadOverpassTagsBeforePostgresProjection \
TestExposureRejectsUnicodeFoldedOverpassTagsBeforePostgresProjection \
TestExposureRejectsUnicodeFoldedGeoBoundarySourceBeforePostgresLoss \
TestExposureRejectsMismatchedGeoBoundaryShapeBeforePostgresLoss"
CMD_TEST_NAMES="TestWorldPopProductionClientRedirectContracts \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放 \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Moved_Permanently \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Found \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/See_Other \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Temporary_Redirect \
TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Permanent_Redirect \
TestWorldPopProductionClientRedirectContracts/轮询_GET_允许同源_HTTPS_重定向"
LIVE_TEST_NAMES="TestLivePopulation TestLiveInfrastructure TestLiveBoundary"

count_exact_line() {
  count_output=$1
  count_expected=$2
  printf "%s\n" "$count_output" | grep -F -x -- "$count_expected" | wc -l | tr -d "[:space:]"
}

count_result_line() {
  count_output=$1
  count_kind=$2
  count_name=$3
  printf "%s\n" "$count_output" |
    grep -E -x -- "[[:space:]]*--- $count_kind: $count_name \\([0-9]+([.][0-9]+)?s\\)" |
    wc -l | tr -d "[:space:]"
}

require_exact_test_pass() {
  gate_output=$1
  gate_name=$2
  run_count=$(count_exact_line "$gate_output" "=== RUN   $gate_name")
  pass_count=$(count_result_line "$gate_output" PASS "$gate_name")
  skip_count=$(count_result_line "$gate_output" SKIP "$gate_name")
  fail_count=$(count_result_line "$gate_output" FAIL "$gate_name")
  if [ "$run_count" -eq 1 ] && [ "$pass_count" -eq 1 ] &&
    [ "$skip_count" -eq 0 ] && [ "$fail_count" -eq 0 ]; then
    return 0
  fi
  printf "%s 门禁失败：RUN=%s PASS=%s SKIP=%s FAIL=%s\n" \
    "$gate_name" "$run_count" "$pass_count" "$skip_count" "$fail_count" >&2
  return 1
}

require_test_passes() {
  gate_output=$1
  gate_names=$2
  for gate_name in $gate_names; do
    if ! require_exact_test_pass "$gate_output" "$gate_name"; then
      return 1
    fi
  done
}

run_postgres_gate() {
  if postgres_output=$(go test ./internal/adapters/storage/postgres \
    -run "^Test(Exposure.*|ReadExposure.*|AdministrativeProjection.*|ProjectAdministration.*|ProjectInfrastructure.*|InfrastructureBinding.*|HasCurrent.*)$" \
    -v -count=1 2>&1); then
    postgres_status=0
  else
    postgres_status=$?
  fi
  printf "%s\n" "$postgres_output"
  if [ "$postgres_status" -ne 0 ]; then
    return "$postgres_status"
  fi
  require_test_passes "$postgres_output" "$POSTGRES_TEST_NAMES"
}

run_cmd_gate() {
  if cmd_output=$(go test ./cmd/server \
    -run "^Test(DefaultExposure.*|BuildExposure.*|NewExposure.*|WorldPopProductionClientRedirectContracts|SpatialRefresh.*Exposure.*|SpatialRefreshFreshPath.*)$" \
    -v -count=1 2>&1); then
    cmd_status=0
  else
    cmd_status=$?
  fi
  printf "%s\n" "$cmd_output"
  if [ "$cmd_status" -ne 0 ]; then
    return "$cmd_status"
  fi
  require_test_passes "$cmd_output" "$CMD_TEST_NAMES"
}

run_live_gate() {
  if live_output=$(go test -p=1 ./internal/adapters/provider/worldpop ./internal/adapters/provider/overpass \
    ./internal/adapters/provider/geoboundaries \
    -run "^TestLive(Population|Infrastructure|Boundary)$" -v -count=1 2>&1); then
    live_status=0
  else
    live_status=$?
  fi
  printf "%s\n" "$live_output"
  if [ "$live_status" -ne 0 ]; then
    return "$live_status"
  fi
  require_test_passes "$live_output" "$LIVE_TEST_NAMES"
}
