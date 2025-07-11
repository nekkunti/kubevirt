package virthandler

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
 * Copyright The KubeVirt Authors.
 *
 */

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"kubevirt.io/kubevirt/pkg/virt-config/featuregate"

	"k8s.io/apimachinery/pkg/types"

	v1 "k8s.io/api/core/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	gomegatypes "github.com/onsi/gomega/types"

	"kubevirt.io/kubevirt/pkg/testutils"
	virtconfig "kubevirt.io/kubevirt/pkg/virt-config"
)

const (
	// Arbitrary values, with memAvailablePressure below 20% of memTotal
	memTotal               = 65680332
	memAvailablePressure   = 5183928
	memAvailableNoPressure = 39207804

	testNodeName = "test-node"
)

var _ = Describe("KSM", func() {
	var fakeSysKSMDir string

	createCustomKSMTree := func() {
		var err error
		fakeSysKSMDir, err = os.MkdirTemp("", "ksm")
		Expect(err).NotTo(HaveOccurred())
		err = os.WriteFile(filepath.Join(fakeSysKSMDir, "run"), []byte("0\n"), 0644)
		Expect(err).NotTo(HaveOccurred())
		err = os.WriteFile(filepath.Join(fakeSysKSMDir, "sleep_millisecs"), []byte("20\n"), 0644)
		Expect(err).NotTo(HaveOccurred())
		err = os.WriteFile(filepath.Join(fakeSysKSMDir, "pages_to_scan"), []byte("100\n"), 0644)
		Expect(err).NotTo(HaveOccurred())
	}

	createCustomMemInfo := func(pressure bool) {
		if filepath.Dir(memInfoPath) == "/tmp" {
			// Not the first custom meminfo, remove the previous one
			err := os.Remove(memInfoPath)
			Expect(err).NotTo(HaveOccurred())
		}
		fakeMemInfo, err := os.CreateTemp("", "meminfo")
		Expect(err).ToNot(HaveOccurred())
		defer fakeMemInfo.Close()
		_, err = fakeMemInfo.WriteString(fmt.Sprintf("MemTotal:       %d kB\n", memTotal))
		Expect(err).NotTo(HaveOccurred())
		if pressure {
			_, err = fakeMemInfo.WriteString(fmt.Sprintf("MemAvailable:    %d kB\n", memAvailablePressure))
		} else {
			_, err = fakeMemInfo.WriteString(fmt.Sprintf("MemAvailable:   %d kB\n", memAvailableNoPressure))
		}
		Expect(err).NotTo(HaveOccurred())
		memInfoPath = fakeMemInfo.Name()
	}

	expectKSMState := func(ksm ksmState) {
		runningS := "0"
		if ksm.running {
			runningS = "1"

			pages, err := os.ReadFile(filepath.Join(fakeSysKSMDir, "pages_to_scan"))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			ExpectWithOffset(1, string(bytes.TrimSpace(pages))).To(Equal(strconv.Itoa(ksm.pages)), "bad pages count")

			sleep, err := os.ReadFile(filepath.Join(fakeSysKSMDir, "sleep_millisecs"))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			ExpectWithOffset(1, string(bytes.TrimSpace(sleep))).To(Equal(strconv.FormatUint(ksm.sleep, 10)), "bad sleep count")
		}
		running, err := os.ReadFile(filepath.Join(fakeSysKSMDir, "run"))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		ExpectWithOffset(1, string(bytes.TrimSpace(running))).To(Equal(runningS), "bad running state")
	}

	BeforeEach(func() {
		createCustomKSMTree()
		ksmBasePath = fakeSysKSMDir + "/"
		ksmRunPath = ksmBasePath + "run"
		ksmSleepPath = ksmBasePath + "sleep_millisecs"
		ksmPagesPath = ksmBasePath + "pages_to_scan"
	})

	AfterEach(func() {
		_ = os.RemoveAll(fakeSysKSMDir)
	})

	When("ksmConfiguration is not provided", func() {
		It("should set KSM label value to false", func() {
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:        testNodeName,
					Labels:      nil,
					Annotations: map[string]string{"unrelated-key": "unrelated-value"},
				},
			}
			fakeClient := fake.NewSimpleClientset(node)
			clusterConfig := generateClusterConfig(featuregate.CPUManager)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, true)
			createCustomMemInfo(false)

			node, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), testNodeName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(node.Labels).To(HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "false"))

			err = os.WriteFile(filepath.Join(fakeSysKSMDir, "run"), []byte("1\n"), 0644)
			Expect(err).ToNot(HaveOccurred())

			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			node, err = fakeClient.CoreV1().Nodes().Get(context.TODO(), testNodeName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(node.Labels).To(HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "false"))
			Expect(node.Annotations).To(HaveKeyWithValue("unrelated-key", "unrelated-value"))
		})
	})

	When("ksmConfiguration is provided,", func() {
		var kv *kubevirtv1.KubeVirt
		var clusterConfig *virtconfig.ClusterConfig

		alternativeLabelSelector := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "test_label",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"true"},
				},
			},
		}
		BeforeEach(func() {
			kv = &kubevirtv1.KubeVirt{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubevirt",
					Namespace: "kubevirt",
				},
				Spec: kubevirtv1.KubeVirtSpec{
					Configuration: kubevirtv1.KubeVirtConfiguration{
						KSMConfiguration: &kubevirtv1.KSMConfiguration{
							NodeLabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"test_label": "true",
								},
							},
						},
					},
				},
			}

			clusterConfig, _, _ = testutils.NewFakeClusterConfigUsingKV(kv)
		})

		DescribeTable("independently from node pressure", func(nodeLabels map[string]string, expectedLabelValue string) {
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   testNodeName,
					Labels: nodeLabels,
				},
			}
			fakeClient := fake.NewSimpleClientset(node)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, true)

			node, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), testNodeName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(node.Labels).To(HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, expectedLabelValue))
		},
			Entry("should add KSM label if the node labels match ksmConfiguration.nodeLabelSelector", map[string]string{"test_label": "true"}, "true"),
			Entry("should not add KSM label if the node labels match ksmConfiguration.nodeLabelSelector", map[string]string{"no_macthing_label": "true"}, "false"),
		)

		DescribeTable("with memory pressure, should", func(initialKsmValue string, selectorOverride *metav1.LabelSelector,
			nodeLabels, nodeAnnotations map[string]string,
			labelsMatcher gomegatypes.GomegaMatcher, annotationsMatcher gomegatypes.GomegaMatcher, expectedKsmValue string) {
			if selectorOverride != nil {
				kv.Spec.Configuration.KSMConfiguration.NodeLabelSelector = selectorOverride
			}
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:        testNodeName,
					Labels:      nodeLabels,
					Annotations: nodeAnnotations,
				},
			}
			fakeClient := fake.NewSimpleClientset(node)
			err := os.WriteFile(filepath.Join(fakeSysKSMDir, "run"), []byte(initialKsmValue), 0644)
			Expect(err).ToNot(HaveOccurred())
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, true)

			createCustomMemInfo(true)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)

			node, err = fakeClient.CoreV1().Nodes().Get(context.TODO(), testNodeName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(node.Labels).To(labelsMatcher)
			Expect(node.Annotations).To(annotationsMatcher)

			running, err := os.ReadFile(filepath.Join(fakeSysKSMDir, "run"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(bytes.TrimSpace(running))).To(Equal(expectedKsmValue))
		},
			Entry("enable ksm if the node labels match ksmConfiguration.nodeLabelSelector",
				"0\n", nil, map[string]string{"test_label": "true"}, make(map[string]string),
				HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "true"), HaveKeyWithValue(kubevirtv1.KSMHandlerManagedAnnotation, "true"),
				"1",
			),
			Entry("disable ksm if the node labels does not match ksmConfiguration.nodeLabelSelector and the node has the KSMHandlerManagedAnnotation annotation",
				"1\n", nil, map[string]string{"test_label": "false"}, map[string]string{kubevirtv1.KSMHandlerManagedAnnotation: "true"},
				HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "false"), HaveKeyWithValue(kubevirtv1.KSMHandlerManagedAnnotation, "false"),
				"0",
			),
			Entry(", with alternative label selector, enable ksm if the node labels match ksmConfiguration.nodeLabelSelector",
				"0\n", alternativeLabelSelector, map[string]string{"test_label": "true"}, make(map[string]string),
				HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "true"), HaveKeyWithValue(kubevirtv1.KSMHandlerManagedAnnotation, "true"),
				"1",
			),
			Entry(", with alternative label selector, disable ksm if the node labels does not match ksmConfiguration.nodeLabelSelector and the node has the KSMHandlerManagedAnnotation annotation",
				"1\n", alternativeLabelSelector, map[string]string{"test_label": "false"}, map[string]string{kubevirtv1.KSMHandlerManagedAnnotation: "true"},
				HaveKeyWithValue(kubevirtv1.KSMEnabledLabel, "false"), HaveKeyWithValue(kubevirtv1.KSMHandlerManagedAnnotation, "false"),
				"0",
			),
		)

		It("should adapt to memory pressure", func() {
			By("initializing with KSM enabled on the node and no memory pressure")
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   testNodeName,
					Labels: map[string]string{"test_label": "true"},
				},
			}
			expected := ksmState{
				running: false,
				sleep:   sleepMsBaselineDefault * (16 * 1024 * 1024) / (memTotal - memAvailablePressure),
				pages:   nPagesInitDefault,
			}
			fakeClient := fake.NewSimpleClientset(node)
			createCustomMemInfo(false)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, true)

			By("running a first HandleKSMUpdate and expecting no change")
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expectKSMState(expected)

			By("inducing memory pressure and expecting KSM to start running")
			createCustomMemInfo(true)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.running = true
			expectKSMState(expected)

			By("expecting the number of pages to scan to increase every HandleKSMUpdate up to max value")
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.pages = nPagesInitDefault + pagesBoostDefault
			expectKSMState(expected)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.pages = nPagesMaxDefault
			expectKSMState(expected)

			By("cancelling memory pressure and expecting more sleep and a decay of the number of pages to scan")
			createCustomMemInfo(false)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.pages = nPagesMaxDefault + pagesDecayDefault
			expected.sleep = sleepMsBaselineDefault * (16 * 1024 * 1024) / (memTotal - memAvailableNoPressure)
			expectKSMState(expected)
			for i := 0; i < 15; i++ {
				HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			}
			expected.pages = nPagesMaxDefault + 16*pagesDecayDefault
			expectKSMState(expected)

			By("expecting KSM to stop running after enough time without memory pressure")
			for i := 0; i < 30; i++ {
				HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			}
			expected.running = false
			expectKSMState(expected)
		})

		It("should use override values if provided", func() {
			By("initializing with KSM enabled on the node and override annotations")
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   testNodeName,
					Labels: map[string]string{"test_label": "true"},
					Annotations: map[string]string{
						kubevirtv1.KSMPagesBoostOverride:      "123",
						kubevirtv1.KSMPagesDecayOverride:      "45", // Out of bounds, should use default: -50
						kubevirtv1.KSMPagesMinOverride:        "166",
						kubevirtv1.KSMPagesMaxOverride:        "789",
						kubevirtv1.KSMPagesInitOverride:       "1011", // Out of bounds, can't use default, so should equal pagesMin
						kubevirtv1.KSMSleepMsBaselineOverride: "1213",
						kubevirtv1.KSMFreePercentOverride:     "1.0",
					},
				},
			}
			expected := ksmState{
				running: true,
				sleep:   1213 * (16 * 1024 * 1024) / (memTotal - memAvailableNoPressure),
				pages:   166,
			}
			fakeClient := fake.NewSimpleClientset(node)
			createCustomMemInfo(false)
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, true)

			By("running a first HandleKSMUpdate and expecting the right values")
			expectKSMState(expected)

			By("expecting the number of pages to scan to increase every HandleKSMUpdate up to max value")
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.pages = 166 + 123
			expectKSMState(expected)
			for i := 0; i < 5; i++ {
				HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			}
			expected.pages = 789
			expectKSMState(expected)

			By("cancelling memory pressure and expecting to decrease pages and stop running when reaching minimum")
			data := []byte(fmt.Sprintf(`{"metadata": { "annotations": {"%s": "%s"}}}`, kubevirtv1.KSMFreePercentOverride, "0.1"))
			_, err := fakeClient.CoreV1().Nodes().Patch(context.Background(), testNodeName, types.StrategicMergePatchType, data, metav1.PatchOptions{})
			Expect(err).NotTo(HaveOccurred())
			HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			expected.pages = 789 - 50
			expectKSMState(expected)
			for i := 0; i < 16; i++ {
				HandleKSMUpdate(testNodeName, fakeClient.CoreV1(), clusterConfig, false)
			}
			expected.running = false
			expectKSMState(expected)
		})
	})
})

func generateClusterConfig(featuregates ...string) *virtconfig.ClusterConfig {
	cfg := &kubevirtv1.KubeVirtConfiguration{
		DeveloperConfiguration: &kubevirtv1.DeveloperConfiguration{
			FeatureGates: featuregates,
		},
	}
	clusterConfig, _, _ := testutils.NewFakeClusterConfigUsingKVConfig(cfg)
	return clusterConfig
}
