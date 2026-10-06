#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Installs the gang scheduler selected by GANG_SCHEDULER (kueue or volcano)
# for TestGangScheduling and records the matching go test flags.

set -o errexit
set -o nounset
set -o pipefail

GANG_SCHEDULER="${GANG_SCHEDULER:-kueue}"
KUEUE_VERSION="${KUEUE_VERSION:-v0.18.2}"
VOLCANO_VERSION="${VOLCANO_VERSION:-v1.15.0}"
GANG_NAMESPACE="ai-conformance-gang-scheduling"

case "${GANG_SCHEDULER}" in
kueue)
  echo "Installing Kueue ${KUEUE_VERSION}..."
  kubectl apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"

  echo "Waiting for Kueue controller manager to be ready..."
  kubectl rollout status deployment -n kueue-system kueue-controller-manager --timeout=5m

  echo "Creating Kueue resources (with retries for webhook readiness)..."
  for i in {1..10}; do
    cat <<EOF | kubectl apply -f - && break
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: e2e-flavor
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: e2e-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: e2e-flavor
      resources:
      - name: "cpu"
        nominalQuota: 10
      - name: "memory"
        nominalQuota: 10Gi
---
apiVersion: v1
kind: Namespace
metadata:
  name: ${GANG_NAMESPACE}
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: e2e-lq
  namespace: ${GANG_NAMESPACE}
spec:
  clusterQueue: e2e-cq
EOF
    echo "Webhook might not be ready yet, retrying in 5 seconds... (${i}/10)"
    sleep 5
  done

  echo "Waiting for ClusterQueue to be active..."
  kubectl wait --for=condition=Active clusterqueue/e2e-cq --timeout=60s

  echo "-gang-job-labels=kueue.x-k8s.io/queue-name=e2e-lq" >>"${E2E_TEST_ARGS_FILE}"
  ;;
volcano)
  echo "Installing Volcano ${VOLCANO_VERSION}..."
  kubectl apply -f "https://raw.githubusercontent.com/volcano-sh/volcano/${VOLCANO_VERSION}/installer/volcano-development.yaml"

  echo "Waiting for Volcano controllers to be ready..."
  kubectl rollout status deployment -n volcano-system volcano-admission --timeout=5m
  kubectl rollout status deployment -n volcano-system volcano-controllers --timeout=5m
  kubectl rollout status deployment -n volcano-system volcano-scheduler --timeout=5m

  echo "Creating test namespace..."
  kubectl create namespace "${GANG_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
  ;;
*)
  echo "Error: unsupported GANG_SCHEDULER=${GANG_SCHEDULER} (supported: kueue, volcano)" >&2
  exit 1
  ;;
esac

echo "-gang-scheduler-name=${GANG_SCHEDULER}" >>"${E2E_TEST_ARGS_FILE}"
echo "-gang-scheduler-namespace=${GANG_NAMESPACE}" >>"${E2E_TEST_ARGS_FILE}"
