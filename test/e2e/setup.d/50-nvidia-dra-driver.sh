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

# Installs the NVIDIA DRA driver used by the accelerator tests
# (TestSecureAcceleratorAccess, TestAcceleratorClusterAutoscaling).

set -o errexit
set -o nounset
set -o pipefail

echo "Labeling GPU nodes for DRA driver..."
kubectl label node --all nvidia.com/gpu.present=true feature.node.kubernetes.io/pci-10de.present=true --overwrite

echo "Installing NVIDIA DRA Driver..."
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
helm repo update
helm upgrade -i nvidia-dra-driver nvidia/nvidia-dra-driver-gpu \
    --namespace nvidia-dra-driver \
    --create-namespace \
    --set gpuResourcesEnabledOverride=true \
    --wait --timeout 10m

echo "Checking ResourceSlices & DeviceClasses:"
kubectl get deviceclasses || true
kubectl get resourceslices -o wide || true
