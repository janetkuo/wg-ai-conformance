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

# Installs Kubeflow Trainer, the AI operator exercised by
# TestRobustCRDControllerOperation (KAR-0063). The test's -operator flag
# defaults to kubeflow-trainer, so no go test flags are recorded.
#
# Runs before 50-gang-scheduler.sh: if Kueue starts before the TrainJob CRD
# exists, it defers its TrainJob integration, and until that is set up Kueue's
# TrainJob webhook rejects every TrainJob with "unsupported runtime".

set -o errexit
set -o nounset
set -o pipefail

KUBEFLOW_TRAINER_VERSION="${KUBEFLOW_TRAINER_VERSION:-2.3.0}"

echo "Installing Kubeflow Trainer ${KUBEFLOW_TRAINER_VERSION}..."
helm upgrade -i kubeflow-trainer oci://ghcr.io/kubeflow/charts/kubeflow-trainer \
    --namespace kubeflow-system \
    --create-namespace \
    --version "${KUBEFLOW_TRAINER_VERSION}" \
    --set runtimes.defaultEnabled=true \
    --wait --timeout 5m

echo "Verifying Kubeflow Trainer CRDs and default runtimes..."
kubectl get crd trainjobs.trainer.kubeflow.org
kubectl get clustertrainingruntime torch-distributed
kubectl rollout status deployment -n kubeflow-system kubeflow-trainer-controller-manager --timeout=5m
