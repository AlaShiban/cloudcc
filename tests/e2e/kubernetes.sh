#!/usr/bin/env bash
# A container unit on Kubernetes, deployed to the emulator's EKS.
#
# The emulator backs an EKS cluster with a real k3s container, so this is not a
# mock: `pulumi up` talks to an actual Kubernetes API server, and the Deployment
# and Service it creates are real objects that a real scheduler acts on. What
# the emulator does not provide is the two paths that surround the cluster, and
# both are compensated here rather than pretended away:
#
#   * Credentials. k3s authenticates its own client certificates; the token
#     `aws eks get-token` returns -- which is what the generated kubeconfig asks
#     for, and what is correct against AWS -- is rejected. So the harness
#     supplies k3s's kubeconfig through CLOUDCC_KUBECONFIG. The same bargain as
#     an RDS instance with no engine behind it.
#
#   * The registry. The emulator's ECR hands out
#     000000000000.dkr.ecr.<region>.amazonaws.com URLs, which resolve nowhere,
#     so a pod cannot pull. The harness imports the locally built image into
#     k3s's own content store and tells the Deployment not to pull.
#
# What is therefore proven here: the compiler's Kubernetes output is accepted by
# a real API server, the pod it describes schedules and becomes ready, and the
# Service in front of it routes to that pod. What is not: pulling from ECR, and
# a pod's AWS identity, which needs IRSA.
#
# Usage:
#   ./tests/e2e/kubernetes.sh [example]        # default: k8s-web
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="${1:-k8s-web}"
UNIT="${2:-web}"
SRC="$REPO_ROOT/examples/$EXAMPLE"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-k8s-XXXXXX")"
OUT="$WORK/compiled"
KEEP="${CLOUDCC_E2E_KEEP:-0}"
CURRENT_OUT=""

cleanup() {
  local status=$?
  if [ -n "$CURRENT_OUT" ] && [ -d "$CURRENT_OUT" ]; then
    ( cd "$CURRENT_OUT" \
        && PULUMI_BACKEND_URL="file://$CURRENT_OUT/.pulumi-state" \
           PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
           pulumi destroy -y --stack local >/dev/null 2>&1 ) || true
  fi
  # The k3s container outlives the stack it belonged to: deleting the EKS
  # cluster is what removes it, and a destroy that failed would otherwise leave
  # a Kubernetes cluster running until somebody noticed.
  for c in $(docker ps -aq --filter "ancestor=rancher/k3s" 2>/dev/null); do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
skip_unless_service eks || fail "the emulator does not serve EKS"

for tool in kubectl docker; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required by this test"
done

log "emulator: $CLOUDCC_EMULATOR_ENDPOINT"

# Anything this app left behind, removed before starting.
#
# Every run gets a fresh Pulumi state directory, and the emulator does not: a
# run that failed before its destroy leaves an ECR repository and two IAM roles
# that the next run's `pulumi up` has never heard of and cannot adopt, so it
# fails on RepositoryAlreadyExists and EntityAlreadyExists. Cleaning up first is
# what makes this test re-runnable, which for a test that takes ten minutes
# matters more than usual.
purge_previous() {
  local app="$1" name
  for name in $(aws_local eks list-clusters --query 'clusters[]' --output text 2>/dev/null); do
    case "$name" in "$app"-*)
      for ng in $(aws_local eks list-nodegroups --cluster-name "$name" \
                    --query 'nodegroups[]' --output text 2>/dev/null); do
        aws_local eks delete-nodegroup --cluster-name "$name" --nodegroup-name "$ng" >/dev/null 2>&1 || true
      done
      aws_local eks delete-cluster --name "$name" >/dev/null 2>&1 || true
      ;;
    esac
  done
  for name in $(aws_local ecr describe-repositories \
                  --query 'repositories[].repositoryName' --output text 2>/dev/null); do
    case "$name" in "$app"-*) aws_local ecr delete-repository --repository-name "$name" --force >/dev/null 2>&1 || true ;; esac
  done
  for name in $(aws_local iam list-roles --query 'Roles[].RoleName' --output text 2>/dev/null); do
    case "$name" in "$app"-*)
      for pol in $(aws_local iam list-attached-role-policies --role-name "$name" \
                     --query 'AttachedPolicies[].PolicyArn' --output text 2>/dev/null); do
        aws_local iam detach-role-policy --role-name "$name" --policy-arn "$pol" >/dev/null 2>&1 || true
      done
      aws_local iam delete-role --role-name "$name" >/dev/null 2>&1 || true
      ;;
    esac
  done
  for c in $(docker ps -aq --filter "ancestor=rancher/k3s" 2>/dev/null); do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
}
log "removing anything a previous run left behind"
purge_previous "$EXAMPLE"

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

log "compiling examples/$EXAMPLE"
"$WORK/cloudcc" "$SRC" -o "$OUT" >/dev/null
APP_OUT="$(app_out "$OUT" "$SRC")"

# The Deployment must name the Kubernetes provider rather than the ambient AWS
# one, and it must carry the pod's resources. Checked before a deploy that takes
# minutes, because both are silent when wrong: a missing provider deploys
# nothing to the cluster, and missing resources gives the pod whatever the
# namespace default happens to be.
grep -q 'provider: kubernetesK8s' "$APP_OUT/index.ts" \
  || fail "the Deployment does not name the Kubernetes provider"
grep -q 'memory: "512Mi"' "$APP_OUT/index.ts" \
  || fail "the pod carries no memory limit; the portable memory: did not reach it"
pass "the generated program describes a Deployment on its own provider"

log "installing the generated project's dependencies"
( cd "$APP_OUT" && npm install --silent --no-audit --no-fund ) || fail "npm install failed"

log "packaging (this builds the unit's image)"
( cd "$APP_OUT" && ./bin/package.sh >/dev/null ) || fail "packaging failed"
IMAGE="cloudcc-$UNIT:latest"
docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "package.sh did not build $IMAGE"
pass "image built: $IMAGE"

# ---------------------------------------------------------------- deploy
#
# In two passes. The cluster has to exist before its kubeconfig can be read, and
# the kubeconfig has to be read before the Kubernetes half can be created --
# because the credentials the generated program would use are the ones k3s
# rejects. `pulumi up --target` gives us that seam without a second program.
CURRENT_OUT="$APP_OUT"
export PULUMI_BACKEND_URL="file://$APP_OUT/.pulumi-state"
export PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator
mkdir -p "$APP_OUT/.pulumi-state"

# Every pulumi command from here runs in the generated project, which is what
# `pulumi` means by a project: it searches upwards for Pulumi.yaml from the
# working directory. Everything else this script touches is an absolute path.
cd "$APP_OUT"
pulumi stack init local 2>/dev/null || pulumi stack select local
pulumi_configure_emulator local

log "creating the cluster (a real k3s container; this takes a minute or two)"
# Excluding the Kubernetes half rather than targeting the AWS half. `--target`
# takes a resource and its *dependents*; there is no way to ask for its
# dependencies, so targeting the cluster leaves out the role and subnets it is
# built on and fails on all three. Excluding what cannot be created yet says the
# same thing in the direction Pulumi supports.
#
# Output kept rather than discarded: a `pulumi up` that fails says why, and a
# harness that throws that away makes the reader run it again by hand.
pulumi up -y --parallel 1 --stack local \
  --exclude "**kubernetes:**" --exclude-dependents >"$WORK/up-cluster.log" 2>&1 \
  || { tail -30 "$WORK/up-cluster.log"; fail "the EKS cluster could not be created"; }

CLUSTER="$(aws_local eks list-clusters --query 'clusters[0]' --output text)"
[ -n "$CLUSTER" ] && [ "$CLUSTER" != "None" ] || fail "no EKS cluster was created"
STATUS="$(aws_local eks describe-cluster --name "$CLUSTER" --query 'cluster.status' --output text)"
[ "$STATUS" = "ACTIVE" ] || fail "cluster $CLUSTER is $STATUS, not ACTIVE"
pass "L4 eks: cluster $CLUSTER is ACTIVE"

ECR_URL="$(pulumi stack output --json --stack local \
  | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_ECR_")) | .value' | head -1)"
[ -n "$ECR_URL" ] || fail "the stack exported no ECR repository URL"

# The Kubernetes container, found by its image.
#
# Both emulators back a cluster with k3s and neither names the container the
# same way: ministack runs it directly as ministack-eks-<region>-<cluster>,
# LocalStack runs it through k3d as k3d-<cluster>-<hash>-server-0. What they
# share is the image, so that is what to look for. Finding it by the published
# port does not work either -- LocalStack proxies the API server through itself,
# so the port belongs to LocalStack rather than to the cluster.
ENDPOINT="$(aws_local eks describe-cluster --name "$CLUSTER" --query 'cluster.endpoint' --output text)"
# Waited for, not checked once. A cluster reports ACTIVE before its container
# has finished starting -- LocalStack answers describe-cluster from its own
# state while k3d is still coming up -- so a single look finds nothing and
# blames the Docker socket for a race.
#
# Matched on the image rather than through --filter ancestor, which resolves
# the argument to an image id and misses a tag it has not seen under that exact
# name.
find_cluster_container() {
  docker ps --format '{{.Names}}\t{{.Image}}' | awk '$2 ~ /k3s/ {print $1}' \
    | grep -- '-server-0$' || docker ps --format '{{.Names}}\t{{.Image}}' \
    | awk '$2 ~ /k3s/ {print $1}' | head -1
}
K3S=""
waited=0
while [ -z "$K3S" ]; do
  K3S="$(find_cluster_container | head -1)"
  [ -n "$K3S" ] && break
  waited=$((waited + 1))
  [ "$waited" -gt 120 ] && break
  sleep 1
done
[ -n "$K3S" ] \
  || fail "the cluster says it is at $ENDPOINT, and after two minutes no container is running a k3s image.
  The emulator needs the Docker socket to back a cluster with a real Kubernetes:
      docker run ... -v /var/run/docker.sock:/var/run/docker.sock <emulator>
  Without it the cluster is a stub -- ACTIVE, with an endpoint nothing listens on."
pass "L4 eks: backed by a real Kubernetes at $K3S"

# The kubeconfig, in the order of how faithful each one is.
#
# First the one the compiler generates: the cluster's own endpoint and an exec
# plugin calling `aws eks get-token`, which is what a program deploying to real
# EKS uses. LocalStack accepts it; ministack's k3s does not, because k3s
# authenticates its own client certificates and has never heard of the token.
#
# TLS verification is skipped rather than the certificate trusted, because the
# certificate is issued for a hostname the emulator advertises and this machine
# cannot resolve. The address is not what is under test.
KUBECONFIG_FILE="$WORK/kubeconfig"
cat > "$KUBECONFIG_FILE" <<KUBECONFIG
apiVersion: v1
clusters:
- cluster: {server: "${ENDPOINT/localhost.localstack.cloud/localhost}", insecure-skip-tls-verify: true}
  name: cloudcc
contexts: [{context: {cluster: cloudcc, user: cloudcc}, name: cloudcc}]
current-context: cloudcc
kind: Config
users:
- name: cloudcc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: ["--endpoint-url", "$CLOUDCC_EMULATOR_ENDPOINT", "eks", "get-token", "--cluster-name", "$CLUSTER"]
KUBECONFIG
export KUBECONFIG="$KUBECONFIG_FILE"

if kubectl get nodes >/dev/null 2>&1; then
  pass "L5 the cluster accepts the credentials a real deployment would use"
else
  log "the cluster rejected an EKS token; falling back to its own client certificate"
  PORT="$(docker port "$K3S" 6443/tcp 2>/dev/null | head -1 | sed 's/.*://')"
  [ -n "$PORT" ] || fail "no EKS token accepted and $K3S publishes no API port"
  docker exec "$K3S" cat /etc/rancher/k3s/k3s.yaml 2>/dev/null \
    | sed "s#https://127.0.0.1:6443#https://127.0.0.1:$PORT#" > "$KUBECONFIG_FILE"
  [ -s "$KUBECONFIG_FILE" ] || fail "could not read a kubeconfig from $K3S"
fi

waited=0
until kubectl get nodes >/dev/null 2>&1; do
  waited=$((waited + 1))
  [ "$waited" -gt 60 ] && fail "the Kubernetes API did not answer in 60s"
  sleep 1
done
pass "L5 the cluster's API answers, and a node is registered"

# The registry.
#
# The image goes into the emulator's own ECR, under the repository the compiler
# provisioned, and the pod pulls it from there -- the same round trip a real
# deployment makes. Side-loading it into the node would be easier and would
# prove nothing about the pull.
#
# Two things have to be arranged for that to work against an emulator, and
# neither is about the compiler. The host must be willing to push over plain
# HTTP, which is a Docker daemon setting (see tests/e2e/localstack.sh). And the
# cluster must resolve the registry's hostname to the emulator: inside the node,
# "localhost" is the node, so the name has to be pointed at the emulator's
# address on the Docker network explicitly.
EMULATOR="$(docker ps --filter publish=4566 --format '{{.Names}}' | head -1)"
[ -n "$EMULATOR" ] || fail "no container publishes the emulator's port"
# One address, and specifically one the cluster can reach. The emulator sits on
# several Docker networks once it has created a cluster of its own, and a
# template that ranges over them concatenates their addresses into nonsense --
# 172.17.0.2172.19.0.5172.18.0.2 is not a host anyone can dial. Preferring a
# network the cluster is also on is what makes the address routable from inside
# it; the first one is the fallback when they share none.
EMULATOR_IP=""
for net in $(docker inspect "$K3S" \
    --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null); do
  EMULATOR_IP="$(docker inspect "$EMULATOR" \
    --format "{{with index .NetworkSettings.Networks \"$net\"}}{{.IPAddress}}{{end}}" 2>/dev/null)"
  [ -n "$EMULATOR_IP" ] && break
done
if [ -z "$EMULATOR_IP" ]; then
  EMULATOR_IP="$(docker inspect "$EMULATOR" \
    --format '{{range $k,$v := .NetworkSettings.Networks}}{{$v.IPAddress}}
{{end}}' 2>/dev/null | grep -m1 .)"
fi
[ -n "$EMULATOR_IP" ] || fail "could not find $EMULATOR's address on the Docker network"

REGISTRY_HOST="$(printf '%s' "$ECR_URL" | cut -d/ -f1)"
REGISTRY_NAME="${REGISTRY_HOST%%:*}"
log "pushing $IMAGE to $ECR_URL"
docker tag "$IMAGE" "$ECR_URL:latest" || fail "could not tag $IMAGE as $ECR_URL:latest"
docker push "$ECR_URL:latest" >"$WORK/push.log" 2>&1 || {
  tail -5 "$WORK/push.log"
  fail "could not push to the emulator's registry.
  Docker refuses plain HTTP unless the registry is listed as insecure. Add it:
      # ~/.colima/default/colima.yaml
      docker:
        insecure-registries:
          - \"$REGISTRY_HOST\"
  then colima restart. Wildcards are rejected; dockerd wants the exact host."
}
pass "image pushed to the emulator's ECR"

log "pointing the cluster at the registry ($REGISTRY_NAME -> $EMULATOR_IP)"
# Inside the node, the registry's name must reach the emulator rather than the
# node itself. The hostname is kept rather than replaced by the address, because
# the emulator routes ECR on the Host header and an address would arrive as a
# name it does not serve.
docker exec -i "$K3S" sh -c "grep -q '$REGISTRY_NAME' /etc/hosts || echo '$EMULATOR_IP $REGISTRY_NAME' >> /etc/hosts" \
  || fail "could not teach the cluster where the registry is"

# Written where containerd reads it per pull, rather than into registries.yaml,
# which k3s reads once at startup. No restart: k3s sets containerd's
# config_path to this directory, so a mirror added here takes effect on the next
# pull -- and the container cannot be restarted anyway once its cluster is
# running.
#
# -i, because docker exec does not forward stdin without it and the heredoc
# would write an empty file, leaving no mirror and a pull that fails against a
# name resolving to the node itself.
CERTS_D="/var/lib/rancher/k3s/agent/etc/containerd/certs.d/$REGISTRY_HOST"
docker exec -i "$K3S" sh -c "mkdir -p '$CERTS_D' && cat > '$CERTS_D/hosts.toml'" <<HOSTS
server = "http://$REGISTRY_HOST"

[host."http://$REGISTRY_HOST"]
  capabilities = ["pull", "resolve"]
HOSTS
pass "the cluster can reach the emulator's registry"

# The cluster has one node and it is the control plane, which Kubernetes taints
# so that workloads do not land on it. Real EKS has worker nodes from a node
# group; this emulator records the node group as an API object and starts no
# machines, so the control plane is all there is.
#
# Removing the taint is the harness saying "treat this single node as the
# cluster", which is what a one-node development cluster means. It is not a
# statement about the application: nothing in the Deployment changes, and on
# real EKS the pods land on workers that were never tainted.
control_plane_taint() {
  kubectl get nodes -o jsonpath='{.items[*].spec.taints[*].key}' 2>/dev/null \
    | grep -c 'node-role.kubernetes.io/control-plane' || true
}
if [ "$(control_plane_taint)" != "0" ]; then
  # Checked by re-reading the node rather than by the command's exit code. The
  # two forms of this command -- with and without the effect suffix -- disagree
  # about what counts as success when there is nothing to remove, and what
  # matters is the state afterwards either way.
  kubectl taint nodes --all node-role.kubernetes.io/control-plane- >"$WORK/untaint.log" 2>&1 || true
  kubectl taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule- >>"$WORK/untaint.log" 2>&1 || true
  if [ "$(control_plane_taint)" != "0" ]; then
    cat "$WORK/untaint.log"
    fail "the cluster's only node is still tainted, so no pod can be scheduled on it"
  fi
  pass "the control-plane node is schedulable (this cluster has no workers)"
fi

log "deploying the Kubernetes half"
if CLOUDCC_KUBECONFIG="$(cat "$KUBECONFIG_FILE")" \
     pulumi up -y --parallel 1 --stack local >"$WORK/up-k8s.log" 2>&1; then
  pass "L4 the Deployment and Service were accepted by the cluster"
else
  # One failure is expected here and is the emulator's, not the program's: a
  # Service of type LoadBalancer waits for a cloud load balancer to allocate an
  # address, and there is no cloud. Pulumi reports that as a failed update even
  # though the Service exists and works.
  #
  # Tolerated only when it is the *only* thing that failed. A blanket "ignore
  # errors here" would hide a Deployment that never started, which is most of
  # what this test is for -- so every error line has to be that one.
  OTHER="$(grep -E '^\s+\*' "$WORK/up-k8s.log" \
    | grep -viE 'was not allocated an IP address|timed out waiting to be Ready' || true)"
  if [ -n "$OTHER" ]; then
    tail -30 "$WORK/up-k8s.log"
    fail "pulumi up failed for something other than the missing load balancer"
  fi
  warn "the LoadBalancer Service was not given an address: the emulator runs no cloud
  load balancer, so this is unverified rather than broken. Everything below reaches
  the Service directly, which is what the address would have been for."
fi

DEPLOY="$(kubectl get deployments -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
[ -n "$DEPLOY" ] || fail "no Deployment exists in the cluster after a successful up"
log "deployment: $DEPLOY"

log "waiting for the pod"
kubectl rollout status "deployment/$DEPLOY" --timeout=180s \
  || { kubectl describe deployment "$DEPLOY" | tail -25; fail "the pod never became ready"; }
pass "L5 the pod is running: the unit's image started under Kubernetes"

READY="$(kubectl get deployment "$DEPLOY" -o jsonpath='{.status.readyReplicas}')"
[ "${READY:-0}" -ge 1 ] || fail "readyReplicas is ${READY:-0}"

# The pod's memory limit is the portable `memory:` from cloudcc.yaml, translated
# into the units Kubernetes writes. Read back from the cluster rather than from
# the generated source, so this is what the API server actually stored.
LIMIT="$(kubectl get deployment "$DEPLOY" \
  -o jsonpath='{.spec.template.spec.containers[0].resources.limits.memory}')"
[ "$LIMIT" = "512Mi" ] || fail "the pod's memory limit is $LIMIT, want 512Mi from memory: 512"
pass "L5 memory: 512 reached the pod as $LIMIT"

SVC="$(kubectl get services -l app="$UNIT" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
[ -n "$SVC" ] || fail "no Service was created for unit $UNIT"
SVC_TYPE="$(kubectl get service "$SVC" -o jsonpath='{.spec.type}')"
[ "$SVC_TYPE" = "LoadBalancer" ] \
  || fail "service $SVC is $SVC_TYPE; an exposed unit should get a LoadBalancer"
pass "L4 the exposed unit's Service is a $SVC_TYPE"

# Through the Service rather than at the pod: a Service that selects nothing is
# a perfectly healthy-looking object, and the difference only shows on a
# request. Port-forwarding is how to reach one without a cloud load balancer.
log "driving the application through its Service"
kubectl port-forward "service/$SVC" 18080:80 >/dev/null 2>&1 &
FORWARD_PID=$!
trap 'kill $FORWARD_PID 2>/dev/null || true' RETURN 2>/dev/null || true
wait_for_http "http://127.0.0.1:18080/health" 45 \
  || { kill $FORWARD_PID 2>/dev/null; fail "the Service did not route to the pod"; }

BODY="$(curl -sf http://127.0.0.1:18080/where || true)"
kill $FORWARD_PID 2>/dev/null || true
echo "$BODY" | grep -q '"host"' || fail "unexpected reply through the Service: $BODY"
pass "L5 the Service routes to the pod: $BODY"

log "tearing down"
CLOUDCC_KUBECONFIG="$(cat "$KUBECONFIG_FILE")" \
  pulumi destroy -y --stack local >/dev/null 2>&1 || true
CURRENT_OUT=""

echo
pass "a container unit ran on Kubernetes, provisioned from the same source that runs on Fargate"
