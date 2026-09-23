#!/usr/bin/env bash
# Woodpecker build/push/deploy for cts-lite -- the CD half of the pipeline.
#
# It replaces the two deploy jobs of the removed GitHub Actions workflow
# (.github/workflows/cicd.yml, last present at c68a30f8^):
#
#   build_and_push_image  download the dataset, build compounds.db, build the
#                         image, push :<sha> and :latest to ECR
#   deploy_to_ecs         force a new ECS deployment, wait for stability
#
#   bash ops/ci/woodpecker-deploy.sh build    # image -> ECR
#   bash ops/ci/woodpecker-deploy.sh deploy   # ECS rollout
#
# The exec hosts have no docker daemon and no aws CLI, so the image is
# assembled daemonlessly by ops/ci/deploy (go-containerregistry + AWS SDK)
# from the same pieces the Dockerfile used:
#
#   base     golang:1.25-trixie, pinned by digest below
#   layer 1  /app/dataset/compounds.db     (COPY dataset/compounds.db ...)
#   layer 2  /app/<source tree> + ctslite  (COPY --exclude=... . . ; go build)
#   config   WORKDIR /app, CMD ["./ctslite"], EXPOSE 8080 -- and build-push
#            refuses to push if that differs from what :latest runs today
#
# The binary is built with the Go release the base image carries
# (GOTOOLCHAIN), on the same Debian release, so it links against the glibc
# and libstdc++ it will find at runtime (rdkit/ is cgo, -lstdc++). The
# Dockerfile's `go mod download` / build-cache layers are not reproduced:
# nothing reads /go/pkg/mod at runtime.
#
# Skip words, as in the GitHub workflow: a commit message containing
# `nobuild` skips build AND deploy (deploy needed the build job there);
# `nodeploy` skips only the ECS rollout (:latest still moves).
#
# DRY_RUN=1 (a variable on a manual pipeline) builds, smoke-tests and pushes
# ONLY :ci-dryrun-<sha>. It never moves :latest and never touches ECS.
#
# Credentials: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from the repo
# secrets deploy_aws_access_key_id / deploy_aws_secret_access_key, which
# Woodpecker injects only on push and manual events. They are read from the
# environment by ops/ci/deploy and never echoed; this script runs without
# xtrace for that reason.
set -euo pipefail
set +x

PHASE="${1:-}"

REPO="702514165722.dkr.ecr.us-west-2.amazonaws.com/cts-lite"
# golang:1.25-trixie as of 2026-09-23 (index digest; linux/amd64 is selected).
BASE="docker.io/library/golang:1.25-trixie@sha256:2c4c60ef415fbfa5e90300722293bef36c5e63fae17570ce18f580af933dbd73"
# The Go release inside BASE (its GOLANG_VERSION). build-push checks they agree.
BASE_GO="1.25.14"
ECS_CLUSTER="services"
ECS_SERVICE="cts-lite"
ECS_TIMEOUT="${CTS_ECS_TIMEOUT:-20m}"

SHA="${CI_COMMIT_SHA:-$(git rev-parse HEAD)}"
MSG="${CI_COMMIT_MESSAGE:-$(git log -1 --format=%B)}"
DRY_RUN="${DRY_RUN:-0}"

# Everything large lives under the pipeline workspace (node-local disk),
# never in a shared /tmp directory. Peak is roughly csv + db + gz layer.
WORK="$PWD/.cd"
DIGEST_FILE="$WORK/image-digest"

step() { echo; echo "=== $* ==="; }

skip_if() {
  local word="$1" what="$2"
  if grep -qi -- "$word" <<<"$MSG"; then
    echo "commit message contains '$word': skipping $what"
    exit 0
  fi
}

tool() { "$WORK/bin/deploy" "$@"; }

build_tool() {
  step "build ops/ci/deploy"
  mkdir -p "$WORK/bin"
  (cd ops/ci/deploy && go build -trimpath -o "$WORK/bin/deploy" .)
}

smoke() {
  local app="$1" db="$2"
  step "smoke test: the built binary against the built compounds.db"
  local port
  port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
  local log="$WORK/smoke.log"
  ( cd "$app" && exec env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY \
      PORT="$port" DB_PATH="$db" OTEL_SDK_DISABLED=true ./ctslite ) >"$log" 2>&1 &
  local pid=$!
  local base="http://127.0.0.1:$port" ok=0
  for _ in $(seq 1 120); do
    if curl -sf -o /dev/null "$base/health"; then ok=1; break; fi
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
  done
  local rc=0
  if [ "$ok" = 1 ]; then
    curl -sf "$base/health" | grep -q 'up and running' || { echo "FATAL: /health body unexpected" >&2; rc=1; }
    curl -sf "$base/" | grep -qi '<html' || { echo "FATAL: / did not serve web/index.html" >&2; rc=1; }
    # Caffeine: an InChIKey that must be in any real dataset, looked up in
    # the database this image will ship, and a SMILES that goes through the
    # rdkit (cgo) conversion path.
    local out
    out=$(curl -sf "$base/match?q=RYYVLZVUVIJVGH-UHFFFAOYSA-N&rdkit_conversion=false") || rc=1
    echo "match(inchikey): ${out:0:300}"
    if ! grep -q 'RYYVLZVUVIJVGH-UHFFFAOYSA-N' <<<"$out" || ! grep -qi 'caffeine' <<<"$out"; then
      echo "FATAL: caffeine not found by InChIKey" >&2; rc=1
    fi
    out=$(curl -sf "$base/match?q=CN1C%3DNC2%3DC1C(%3DO)N(C(%3DO)N2C)C") || rc=1
    echo "match(smiles):   ${out:0:300}"
    grep -q 'RYYVLZVUVIJVGH-UHFFFAOYSA-N' <<<"$out" \
      || { echo "FATAL: caffeine SMILES did not resolve to its InChIKey" >&2; rc=1; }
  else
    echo "FATAL: server never answered /health" >&2
    rc=1
  fi
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  echo "--- server log ---"; tail -n 40 "$log"
  [ "$rc" = 0 ] && echo "smoke test passed"
  return "$rc"
}

# Only main ships. A manual pipeline on a PR branch may dry-run, nothing more.
guard_branch() {
  local branch="${CI_COMMIT_BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"
  if [ "$DRY_RUN" != 1 ] && [ "$branch" != main ]; then
    echo "FATAL: refusing to publish from branch '$branch'; only main deploys (set DRY_RUN=1 for a dry run)" >&2
    exit 1
  fi
}

build() {
  skip_if nobuild "the image build and the deploy"
  guard_branch
  local tag="$SHA"
  if [ "$DRY_RUN" = 1 ]; then tag="ci-dryrun-$SHA"; fi
  echo "commit: $SHA  tag: $tag  dry-run: $DRY_RUN"

  rm -rf "$WORK"
  mkdir -p "$WORK"
  # Large intermediates go even on failure; the digest file stays for deploy.
  trap 'rm -rf "$WORK/l1" "$WORK/l2" "$WORK"/*.tar.gz dataset/cts-lite_latest.csv dataset/cts-lite_latest.csv.part' EXIT

  build_tool
  export GOTOOLCHAIN="go$BASE_GO"
  step "go toolchain (the base image's release)"
  go version

  step "dataset"
  df -h . | tail -1
  tool fetch-dataset -out dataset/cts-lite_latest.csv

  step "build compounds.db"
  rm -f dataset/compounds.db
  (cd dataset && go run ./cmd/build-db/build-db.go cts-lite_latest.csv compounds.db)
  # Normalised mtime, as the GitHub job did for layer caching.
  touch -t 197001010000 dataset/compounds.db
  rm -v dataset/cts-lite_latest.csv
  ls -l dataset/compounds.db

  step "stage layers"
  local l1="$WORK/l1" l2="$WORK/l2"
  mkdir -p "$l1/app/dataset" "$l2/app"
  mv dataset/compounds.db "$l1/app/dataset/compounds.db"
  # The build context, filtered like .dockerignore does (dataset/, .git/,
  # .github/, .gitignore), taken from git so no stray workspace file ships.
  git ls-files -z | grep -zvE '^(dataset/|\.github/|\.gitignore$)' \
    | xargs -0 cp --parents -t "$l2/app"
  go build -o "$l2/app/ctslite" ./server
  file "$l2/app/ctslite"
  ldd "$l2/app/ctslite"

  smoke "$l2/app" "$l1/app/dataset/compounds.db"

  step "layer tarballs"
  local t
  for t in l1 l2; do
    tar --sort=name --mtime='1970-01-01 00:00:00Z' --owner=0 --group=0 --numeric-owner \
      -C "$WORK/$t" -cf - app | gzip -1 >"$WORK/$t.tar.gz"
    ls -l "$WORK/$t.tar.gz"
  done
  rm -rf "$l1" "$l2"

  step "push $REPO:$tag"
  tool build-push -base "$BASE" -expect-go "$BASE_GO" -repo "$REPO" -tag "$tag" \
    -layer "$WORK/l1.tar.gz" -layer "$WORK/l2.tar.gz" -digest-out "$DIGEST_FILE"

  if [ "$DRY_RUN" = 1 ]; then
    echo "DRY_RUN=1: pushed $REPO:$tag only; :latest and ECS untouched"
    return 0
  fi
  step "move :latest"
  tool tag -repo "$REPO" -digest "$(cat "$DIGEST_FILE")" -to latest
}

deploy() {
  skip_if nobuild "the deploy (no image was built)"
  skip_if nodeploy "the ECS deployment"
  if [ "$DRY_RUN" = 1 ]; then
    echo "DRY_RUN=1: not touching ECS"
    exit 0
  fi
  guard_branch
  test -s "$DIGEST_FILE" || { echo "FATAL: no image digest from the build step" >&2; exit 1; }
  build_tool
  step "ECS rollout $ECS_CLUSTER/$ECS_SERVICE"
  tool ecs-deploy -cluster "$ECS_CLUSTER" -service "$ECS_SERVICE" \
    -expect-digest "$(cat "$DIGEST_FILE")" -timeout "$ECS_TIMEOUT"
}

case "$PHASE" in
  build)  build ;;
  deploy) deploy ;;
  *) echo "usage: $0 build|deploy" >&2; exit 2 ;;
esac
