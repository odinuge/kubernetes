/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e_node

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	kubeletconfig "k8s.io/kubernetes/pkg/kubelet/apis/config"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// makeHugePagePod returns a pod that requests the the given amount of huge page memory, and execute the given command
func makeHugePagePod(baseName string, command string, totalHugePageMemory resource.Quantity, hugePageSize resource.Quantity) *apiv1.Pod {
	e2elog.Logf("Pod to run command: %v", command)
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod" + string(uuid.NewUUID()),
		},
		Spec: apiv1.PodSpec{
			RestartPolicy: apiv1.RestartPolicyNever,
			Containers: []apiv1.Container{
				{
					Image:   imageutils.GetE2EImage(imageutils.HugePageTester),
					Name:    "container" + string(uuid.NewUUID()),
					Command: []string{"sh", "-c", command},
					Resources: apiv1.ResourceRequirements{
						Limits: apiv1.ResourceList{
							apiv1.ResourceName("cpu"):                                resource.MustParse("10m"),
							apiv1.ResourceName("memory"):                             resource.MustParse("100Mi"),
							apiv1.ResourceName("hugepages-" + hugePageSize.String()): totalHugePageMemory,
						},
					},
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "hugetlb",
							MountPath: "/hugetlb",
						},
					},
				},
			},
			Volumes: []apiv1.Volume{
				{
					Name: "hugetlb",
					VolumeSource: apiv1.VolumeSource{
						EmptyDir: &apiv1.EmptyDirVolumeSource{Medium: "HugePages"},
					},
				},
			},
		},
	}
	return pod
}

// enableHugePagesInKubelet enables hugepages feature for kubelet
func enableHugePagesInKubelet(f *framework.Framework) *kubeletconfig.KubeletConfiguration {
	oldCfg, err := getCurrentKubeletConfig()
	framework.ExpectNoError(err)
	newCfg := oldCfg.DeepCopy()
	if newCfg.FeatureGates == nil {
		newCfg.FeatureGates = make(map[string]bool)
		newCfg.FeatureGates["HugePages"] = true
	}

	// Update the Kubelet configuration.
	framework.ExpectNoError(setKubeletConfiguration(f, newCfg))

	// Wait for the Kubelet to be ready.
	Eventually(func() bool {
		nodeList := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
		return len(nodeList.Items) == 1
	}, time.Minute, time.Second).Should(BeTrue())

	return oldCfg
}

// configureHugePages attempts to allocate _pageCount_ hugepages of the default hugepage size for testing purposes
func configureHugePages(pageCount int64) error {
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("echo %d > /proc/sys/vm/nr_hugepages", pageCount)).Run()
	if err != nil {
		return err
	}
	outData, err := exec.Command("/bin/sh", "-c", "cat /proc/meminfo | grep 'HugePages_Total' | awk '{print $2}'").Output()
	if err != nil {
		return err
	}
	numHugePages, err := strconv.Atoi(strings.TrimSpace(string(outData)))
	if err != nil {
		return err
	}
	e2elog.Logf("HugePages_Total is set to %v", numHugePages)
	if int64(numHugePages) == pageCount {
		return nil
	}
	return fmt.Errorf("expected hugepages %v, but found %v", pageCount, numHugePages)
}

// releaseHugePages releases all pre-allocated hugepages
func releaseHugePages() error {
	return exec.Command("/bin/sh", "-c", "echo 0 > /proc/sys/vm/nr_hugepages").Run()
}

// getDefaultHugePageSize returns the default huge page size, and a boolean if huge pages are supported
func getDefaultHugePageSize() (resource.Quantity, bool) {
	outData, err := exec.Command("/bin/sh", "-c", "cat /proc/meminfo | grep 'Hugepagesize:' | awk '{print $2}'").Output()
	framework.ExpectNoError(err)
	pageSize, err := strconv.Atoi(strings.TrimSpace(string(outData)))
	framework.ExpectNoError(err)
	if pageSize == 0 {
		return resource.Quantity{}, false
	}
	return *resource.NewQuantity(int64(pageSize*1024), resource.BinarySI), true
}

func getTestValues() (hugePageSize resource.Quantity, totalMemory resource.Quantity, pageCount int64) {
	hugePageSize, _ = getDefaultHugePageSize()
	// If huge page size is  equal to bigger than 1GB, only use two pages
	if hugePageSize.Value() >= (1 << 30) {
		pageCount = 2
	} else {
		pageCount = 20
	}
	totalMemory = *resource.NewQuantity(hugePageSize.Value()*pageCount, resource.BinarySI)
	return
}

// pollResourceAsString polls for a specified resource and capacity from node
func pollResourceAsString(f *framework.Framework, resourceName string) string {
	node, err := f.ClientSet.CoreV1().Nodes().Get(framework.TestContext.NodeName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	amount := amountOfResourceAsString(node, resourceName)
	e2elog.Logf("amount of %v: %v", resourceName, amount)
	return amount
}

// amountOfResourceAsString returns the amount of resourceName advertised by a node
func amountOfResourceAsString(node *apiv1.Node, resourceName string) string {
	val, ok := node.Status.Capacity[apiv1.ResourceName(resourceName)]
	if !ok {
		return ""
	}
	return val.String()
}

func runHugePagesTests(f *framework.Framework) {
	fileName := "/hugetlb/file"
	It("should assign hugepages as expected based on the Pod spec", func() {
		hugePageSize, totalHugePageMemory, _ := getTestValues()
		By("running a pod that requests hugepages and allocates the memory")
		command := fmt.Sprintf(`./hugetlb-tester %d %d %s`, totalHugePageMemory.Value(), hugePageSize.Value(), fileName)

		verifyPod := makeHugePagePod("hugepage-pod", command, totalHugePageMemory, hugePageSize)
		f.PodClient().Create(verifyPod)
		err := framework.WaitForPodSuccessInNamespace(f.ClientSet, verifyPod.Name, f.Namespace.Name)
		By("checking that pod execution succeeded")
		Expect(err).NotTo(HaveOccurred())

	})
	It("should not be possible to allocate more hugepage memory than the Pod spec", func() {
		hugePageSize, totalHugePageMemory, _ := getTestValues()
		By("running a pod that requests hugepages and allocates twice the amount of the requested memory")
		command := fmt.Sprintf(`./hugetlb-tester %d %d %s`, totalHugePageMemory.Value()*2, hugePageSize.Value(), fileName)

		verifyPod := makeHugePagePod("hugepage-pod", command, totalHugePageMemory, hugePageSize)
		f.PodClient().Create(verifyPod)
		err := framework.WaitForPodSuccessInNamespace(f.ClientSet, verifyPod.Name, f.Namespace.Name)
		By("checking that pod execution failed")
		Expect(err).To(HaveOccurred())
	})
	It("should not be possible to allocate hugepage memory with a huge page size not requested in the Pod spec", func() {
		hugePageSize, totalHugePageMemory, _ := getTestValues()
		By("running a pod that requests hugepages and allocates using a page size euqal to twice the requested size")
		command := fmt.Sprintf(`./hugetlb-tester %d %d %s`, totalHugePageMemory.Value(), hugePageSize.Value()*2, fileName)

		verifyPod := makeHugePagePod("hugepage-pod", command, totalHugePageMemory, hugePageSize)
		f.PodClient().Create(verifyPod)
		err := framework.WaitForPodSuccessInNamespace(f.ClientSet, verifyPod.Name, f.Namespace.Name)
		By("checking that pod execution failed")
		Expect(err).To(HaveOccurred())

	})
}

// Serial because the test updates kubelet configuration.
var _ = SIGDescribe("HugePages [Serial] [Feature:HugePages][NodeFeature:HugePages]", func() {
	f := framework.NewDefaultFramework("hugepages-test")

	Context("With config updated with hugepages feature enabled", func() {
		var oldCfg *kubeletconfig.KubeletConfiguration

		BeforeEach(func() {
			By("verifying hugepages are supported")

			hugePageSize, supported := getDefaultHugePageSize()
			_, totalHugePageMemory, pageCount := getTestValues()
			if !supported {
				framework.Skipf("skipping test because hugepages are not supported")
				return
			}
			By("configuring the host to reserve a number of pre-allocated hugepages")
			Eventually(func() error {
				err := configureHugePages(pageCount)
				if err != nil {
					return err
				}
				return nil
			}, 30*time.Second, framework.Poll).Should(BeNil())
			By("enabling hugepages in kubelet")
			oldCfg = enableHugePagesInKubelet(f)
			By("restarting kubelet to pick up pre-allocated hugepages")
			restartKubelet()
			By("by waiting for hugepages resource to become available on the local node")
			Eventually(func() string {
				return pollResourceAsString(f, "hugepages-"+hugePageSize.String())
			}, 30*time.Second, framework.Poll).Should(Equal(totalHugePageMemory.String()))
		})

		runHugePagesTests(f)

		AfterEach(func() {
			By("Releasing hugepages")
			Eventually(func() error {
				err := releaseHugePages()
				if err != nil {
					return err
				}
				return nil
			}, 30*time.Second, framework.Poll).Should(BeNil())
			if oldCfg != nil {
				By("Restoring old kubelet config")
				setOldKubeletConfig(f, oldCfg)
			}
			By("restarting kubelet to release hugepages")
			restartKubelet()
			By("by waiting for hugepages resource to not appear available on the local node")
			hugePageSize, _ := getDefaultHugePageSize()
			Eventually(func() string {
				return pollResourceAsString(f, "hugepages-"+hugePageSize.String())
			}, 30*time.Second, framework.Poll).Should(Equal("0"))
		})
	})
})
