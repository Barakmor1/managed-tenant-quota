package tests

import (
	"context"
	"fmt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/kubevirt/pkg/controller"
	"kubevirt.io/kubevirt/tests"
	"kubevirt.io/kubevirt/tests/libvmi"
	"kubevirt.io/kubevirt/tests/testsuite"
	"kubevirt.io/managed-tenant-quota/tests/framework"
	"time"
)

var _ = Describe("Blocked migration", func() {
	f := framework.NewFramework("fake-test")
	conditionManager := controller.NewVirtualMachineInstanceMigrationConditionManager()

	DescribeTable("single blocked migration", func(overcommitmentResource v1.ResourceName) {
		opts := []libvmi.Option{
			libvmi.WithInterface(libvmi.InterfaceDeviceWithMasqueradeBinding()),
			libvmi.WithNetwork(kv1.DefaultPodNetwork()),
			libvmi.WithNamespace(f.Namespace.GetName()),
		}

		switch overcommitmentResource {
		case v1.ResourceLimitsMemory:
			opts = append(opts, libvmi.WithLimitMemory("512Mi"))
		case v1.ResourceLimitsCPU:
			opts = append(opts, libvmi.WithLimitCPU("2"))
		}

		vmi := libvmi.NewAlpine(opts...)
		vmi = tests.RunVMIAndExpectLaunch(vmi, 30)
		vmiPod := tests.GetRunningPodByVirtualMachineInstance(vmi, testsuite.GetTestNamespace(vmi))
		podResources, err := getCurrLauncherUsage(vmiPod)
		Expect(err).To(Not(HaveOccurred()))
		resourceQuota := NewQuotaBuilder().
			WithNamespace(f.Namespace.GetName()).
			WithName("test-quota").
			WithResource(overcommitmentResource, podResources[overcommitmentResource]).
			Build()

		fmt.Printf("resource in vmi:%+v", podResources[overcommitmentResource])
		vmmrq := NewVmmrqBuilder().
			WithNamespace(f.Namespace.GetName()).
			WithName("test-vmmrq").
			WithResource(overcommitmentResource, podResources[overcommitmentResource]).
			Build()
		_, err = f.K8sClient.CoreV1().ResourceQuotas(resourceQuota.Namespace).Create(context.TODO(), resourceQuota, metav1.CreateOptions{})
		Expect(err).To(Not(HaveOccurred()))

		// execute a migration, wait for finalized state
		By("Starting the Migration")
		migration := tests.NewRandomMigration(vmi.Name, vmi.Namespace)
		migration = tests.RunMigration(f.VirtClient, migration)
		Eventually(func() error {
			migration, err := f.VirtClient.VirtualMachineInstanceMigration(migration.Namespace).Get(migration.Name, &metav1.GetOptions{})
			if err != nil {
				return err
			}
			if conditionManager.HasCondition(migration, kv1.VirtualMachineInstanceMigrationRejectedByResourceQuota) {
				return nil
			}
			return fmt.Errorf("migration is in the phase: %s", migration.Status.Phase)
		}, 60*time.Second, 1*time.Second).ShouldNot(HaveOccurred(), fmt.Sprintf("migration should succeed after %d s", 20*time.Second))

		_, err = f.MtqClient.MtqV1alpha1().VirtualMachineMigrationResourceQuotas(vmmrq.Namespace).Create(context.TODO(), vmmrq, metav1.CreateOptions{})
		Expect(err).To(Not(HaveOccurred()))

		Eventually(func() error {
			migration, err := f.VirtClient.VirtualMachineInstanceMigration(migration.Namespace).Get(migration.Name, &metav1.GetOptions{})
			if err != nil {
				return err
			}
			if !conditionManager.HasCondition(migration, kv1.VirtualMachineInstanceMigrationRejectedByResourceQuota) {
				return nil
			}
			return fmt.Errorf("migration is still blocked in the phase: %s", migration.Status.Phase)
		}, 20*time.Second, 1*time.Second).ShouldNot(HaveOccurred(), fmt.Sprintf("migration be unlocked after %d s", 20*time.Second))

	},
		Entry("vmi memory overcommitment", v1.ResourceMemory),
		Entry("vmi memory requirement overcommitment", v1.ResourceRequestsMemory),
		Entry("vmi memory limit overcommitment", v1.ResourceLimitsMemory),
		Entry("vmi cpu overcommitment", v1.ResourceCPU),
		Entry("vmi cpu requirement overcommitment", v1.ResourceRequestsCPU),
		Entry("vmi cpu limit overcommitment", v1.ResourceLimitsCPU),
		Entry("vmi ephemeralStorage overcommitment", v1.ResourceEphemeralStorage),
		Entry("vmi ephemeralStorage request overcommitment", v1.ResourceRequestsEphemeralStorage),
	)

})