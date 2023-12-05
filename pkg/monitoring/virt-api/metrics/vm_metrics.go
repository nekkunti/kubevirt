/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2023 Red Hat, Inc.
 *
 */

package metrics

import (
	"github.com/machadovilaca/operator-observability/pkg/operatormetrics"
	v1 "kubevirt.io/api/core/v1"
)

var (
	vmMetrics = []operatormetrics.Metric{
		vmsCreatedCounter,
	}

	vmsCreatedCounter = operatormetrics.NewCounterVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vm_created_total",
			Help: "Amount of VMs created, broken down by namespace, since install.",
		},
		[]string{"namespace"},
	)
)

func NewVMCreated(vm *v1.VirtualMachine) {
	vmsCreatedCounter.WithLabelValues(vm.Namespace).Inc()
}
