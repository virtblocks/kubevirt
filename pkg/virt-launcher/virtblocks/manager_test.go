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
 * Copyright 2017 Red Hat, Inc.
 *
 */

package virtblocks

import (
	"encoding/base64"
	"io/ioutil"
	"os"
	"os/user"

	"github.com/golang/mock/gomock"
	"github.com/libvirt/libvirt-go"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/log"
	cloudinit "kubevirt.io/kubevirt/pkg/cloud-init"
	cmdv1 "kubevirt.io/kubevirt/pkg/handler-launcher-com/cmd/v1"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/network"
)

var _ = Describe("Manager", func() {
	var mockVirtBlocks *MockVirtBlocks
	var mockDomain *MockVirtBlockDomain
	var ctrl *gomock.Controller
	testVmName := "testvmi"
	testNamespace := "testnamespace"

	log.Log.SetIOWriter(GinkgoWriter)

	tmpDir, _ := ioutil.TempDir("", "cloudinittest")
	owner, err := user.Current()
	if err != nil {
		panic(err)
	}
	isoCreationFunc := func(isoOutFile, volumeID string, inDir string) error {
		_, err := os.Create(isoOutFile)
		return err
	}
	BeforeSuite(func() {
		err := cloudinit.SetLocalDirectory(tmpDir)
		if err != nil {
			panic(err)
		}
		cloudinit.SetLocalDataOwner(owner.Username)
		cloudinit.SetIsoCreationFunction(isoCreationFunc)
	})

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockVirtBlocks = NewMockVirtBlocks(ctrl)
		mockDomain = NewMockVirtBlockDomain(ctrl)
	})

	expectIsolationDetectionForVMI := func(vmi *v1.VirtualMachineInstance) *api.Domain {
		domain := &api.Domain{}
		c := &api.ConverterContext{
			VirtualMachine: vmi,
			UseEmulation:   true,
			SMBios:         &cmdv1.SMBios{},
		}
		Expect(api.Convert_v1_VirtualMachine_To_api_Domain(vmi, domain, c)).To(Succeed())
		api.SetObjectDefaults_Domain(domain)

		return domain
	}

	Context("on successful VirtualMachineInstance sync", func() {
		It("should define and start a new VirtualMachineInstance", func() {
			// Make sure that we always free the domain after use
			StubOutNetworkForTest()
			vmi := newVMI(testNamespace, testVmName)
			mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, libvirt.Error{Code: libvirt.ERR_NO_DOMAIN})

			domainSpec := expectIsolationDetectionForVMI(vmi)

			//mockDomain.EXPECT().GetState().Return(api.Shutdown, api.ReasonUser, nil)
			mockDomain.EXPECT().Create(gomock.Any()).Return(nil)
			mockDomain.EXPECT().Spec().Return(domainSpec, nil)
			manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
			newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
			Expect(err).To(BeNil())
			Expect(newspec).ToNot(BeNil())
		})
		It("should define and start a new VirtualMachineInstance with userData", func() {
			// Make sure that we always free the domain after use
			StubOutNetworkForTest()
			vmi := newVMI(testNamespace, testVmName)
			mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, libvirt.Error{Code: libvirt.ERR_NO_DOMAIN})

			userData := "fake\nuser\ndata\n"
			networkData := ""
			addCloudInitDisk(vmi, userData, networkData)
			domainSpec := expectIsolationDetectionForVMI(vmi)
			mockDomain.EXPECT().Create(gomock.Any()).Return(nil)
			mockDomain.EXPECT().Spec().Return(domainSpec, nil)
			manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
			newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
			Expect(err).To(BeNil())
			Expect(newspec).ToNot(BeNil())
		})
		It("should define and start a new VirtualMachineInstance with userData and networkData", func() {
			// Make sure that we always free the domain after use
			StubOutNetworkForTest()
			vmi := newVMI(testNamespace, testVmName)
			mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, libvirt.Error{Code: libvirt.ERR_NO_DOMAIN})
			userData := "fake\nuser\ndata\n"
			networkData := "FakeNetwork"
			addCloudInitDisk(vmi, userData, networkData)
			domainSpec := expectIsolationDetectionForVMI(vmi)
			mockDomain.EXPECT().Create(gomock.Any()).Return(nil)
			mockDomain.EXPECT().Spec().Return(domainSpec, nil)
			manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
			newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
			Expect(err).To(BeNil())
			Expect(newspec).ToNot(BeNil())
		})
		It("should leave a defined and started VirtualMachineInstance alone", func() {
			// Make sure that we always free the domain after use
			vmi := newVMI(testNamespace, testVmName)
			domainSpec := expectIsolationDetectionForVMI(vmi)

			mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, nil)
			mockDomain.EXPECT().IsAlive().Return(true, nil)
			mockDomain.EXPECT().IsPaused().Return(false, nil)
			mockDomain.EXPECT().Spec().Return(domainSpec, nil)
			manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
			newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
			Expect(err).To(BeNil())
			Expect(newspec).ToNot(BeNil())
		})
		It("should try to start a VirtualMachineInstance in non-running state",
			func() {
				vmi := newVMI(testNamespace, testVmName)
				domainSpec := expectIsolationDetectionForVMI(vmi)

				mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, nil)
				mockDomain.EXPECT().IsAlive().Return(false, nil)
				mockDomain.EXPECT().Create(gomock.Any()).Return(nil)
				mockDomain.EXPECT().Spec().Return(domainSpec, nil)
				manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
				newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
				Expect(err).To(BeNil())
				Expect(newspec).ToNot(BeNil())
			})
		It("should resume a paused VirtualMachineInstance", func() {
			// Make sure that we always free the domain after use
			vmi := newVMI(testNamespace, testVmName)
			domainSpec := expectIsolationDetectionForVMI(vmi)

			mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, nil)
			mockDomain.EXPECT().IsAlive().Return(true, nil)
			mockDomain.EXPECT().IsPaused().Return(true, nil)
			mockDomain.EXPECT().Resume().Return(nil)
			mockDomain.EXPECT().Spec().Return(domainSpec, nil)
			manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
			newspec, err := manager.SyncVMI(vmi, true, &cmdv1.VirtualMachineOptions{VirtualMachineSMBios: &cmdv1.SMBios{}})
			Expect(err).To(BeNil())
			Expect(newspec).ToNot(BeNil())
		})
	})

	Context("on successful VirtualMachineInstance kill", func() {
		It("should try to destroy a VirtualMachineInstance in alive state",
			func() {
				vmi := newVMI(testNamespace, testVmName)
				mockVirtBlocks.EXPECT().GetDomain().Return(mockDomain, nil)
				mockDomain.EXPECT().IsAlive().Return(true, nil)
				mockDomain.EXPECT().Destroy().Return(nil)
				manager, _ := NewVirtBlocksDomainManager(mockVirtBlocks, "fake", nil, 0)
				err := manager.KillVMI(vmi)
				Expect(err).To(BeNil())
			})
	})

	AfterEach(func() {
		ctrl.Finish()
	})
})

var _ = Describe("resourceNameToEnvvar", func() {
	It("handles resource name with dots and slashes", func() {
		Expect(resourceNameToEnvvar("intel.com/sriov_test")).To(Equal("PCIDEVICE_INTEL_COM_SRIOV_TEST"))
	})
})

var _ = Describe("getSRIOVPCIAddresses", func() {
	getSRIOVInterfaceList := func() []v1.Interface {
		return []v1.Interface{
			v1.Interface{
				Name: "testnet",
				InterfaceBindingMethod: v1.InterfaceBindingMethod{
					SRIOV: &v1.InterfaceSRIOV{},
				},
			},
		}
	}

	It("returns empty map when empty interfaces", func() {
		Expect(len(getSRIOVPCIAddresses([]v1.Interface{}))).To(Equal(0))
	})
	It("returns empty map when interface is not sriov", func() {
		ifaces := []v1.Interface{v1.Interface{Name: "testnet"}}
		Expect(len(getSRIOVPCIAddresses(ifaces))).To(Equal(0))
	})
	It("returns map with empty device id list when variables are not set", func() {
		addrs := getSRIOVPCIAddresses(getSRIOVInterfaceList())
		Expect(len(addrs)).To(Equal(1))
		Expect(len(addrs["testnet"])).To(Equal(0))
	})
	It("gracefully handles a single address value", func() {
		os.Setenv("PCIDEVICE_INTEL_COM_TESTNET_POOL", "0000:81:11.1")
		os.Setenv("KUBEVIRT_RESOURCE_NAME_testnet", "intel.com/testnet_pool")
		addrs := getSRIOVPCIAddresses(getSRIOVInterfaceList())
		Expect(len(addrs)).To(Equal(1))
		Expect(addrs["testnet"][0]).To(Equal("0000:81:11.1"))
	})
	It("returns multiple PCI addresses", func() {
		os.Setenv("PCIDEVICE_INTEL_COM_TESTNET_POOL", "0000:81:11.1,0001:02:00.0")
		os.Setenv("KUBEVIRT_RESOURCE_NAME_testnet", "intel.com/testnet_pool")
		addrs := getSRIOVPCIAddresses(getSRIOVInterfaceList())
		Expect(len(addrs["testnet"])).To(Equal(2))
		Expect(addrs["testnet"][0]).To(Equal("0000:81:11.1"))
		Expect(addrs["testnet"][1]).To(Equal("0001:02:00.0"))
	})
})

var _ = Describe("getEnvAddressListByPrefix with gpu prefix", func() {
	It("returns empty if PCI address is not set", func() {
		Expect(len(getEnvAddressListByPrefix(gpuEnvPrefix))).To(Equal(0))
	})

	It("returns single PCI address ", func() {
		os.Setenv("GPU_PASSTHROUGH_DEVICES_SOME_VENDOR", "2609:19:90.0,")
		addrs := getEnvAddressListByPrefix(gpuEnvPrefix)
		Expect(len(addrs)).To(Equal(1))
		Expect(addrs[0]).To(Equal("2609:19:90.0"))
	})

	It("returns multiple PCI addresses", func() {
		os.Setenv("GPU_PASSTHROUGH_DEVICES_SOME_VENDOR", "2609:19:90.0,2609:19:90.1")
		addrs := getEnvAddressListByPrefix(gpuEnvPrefix)
		Expect(len(addrs)).To(Equal(2))
		Expect(addrs[0]).To(Equal("2609:19:90.0"))
		Expect(addrs[1]).To(Equal("2609:19:90.1"))
	})
})

var _ = Describe("getEnvAddressListByPrefix with vgpu prefix", func() {
	It("returns empty if Mdev Uuid is not set", func() {
		Expect(len(getEnvAddressListByPrefix(vgpuEnvPrefix))).To(Equal(0))
	})

	It("returns single  Mdev Uuid ", func() {
		os.Setenv("VGPU_PASSTHROUGH_DEVICES_SOME_VENDOR", "aa618089-8b16-4d01-a136-25a0f3c73123,")
		addrs := getEnvAddressListByPrefix(vgpuEnvPrefix)
		Expect(len(addrs)).To(Equal(1))
		Expect(addrs[0]).To(Equal("aa618089-8b16-4d01-a136-25a0f3c73123"))
	})

	It("returns multiple  Mdev Uuid", func() {
		os.Setenv("VGPU_PASSTHROUGH_DEVICES_SOME_VENDOR", "aa618089-8b16-4d01-a136-25a0f3c73123,aa618089-8b16-4d01-a136-25a0f3c73124")
		addrs := getEnvAddressListByPrefix(vgpuEnvPrefix)
		Expect(len(addrs)).To(Equal(2))
		Expect(addrs[0]).To(Equal("aa618089-8b16-4d01-a136-25a0f3c73123"))
		Expect(addrs[1]).To(Equal("aa618089-8b16-4d01-a136-25a0f3c73124"))
	})

})

func newVMI(namespace, name string) *v1.VirtualMachineInstance {
	vmi := v1.NewMinimalVMIWithNS(namespace, name)
	v1.SetObjectDefaults_VirtualMachineInstance(vmi)
	return vmi
}

func StubOutNetworkForTest() {
	network.SetupPodNetwork = func(vm *v1.VirtualMachineInstance, domain *api.Domain) error { return nil }
}

func addCloudInitDisk(vmi *v1.VirtualMachineInstance, userData string, networkData string) {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name:  "cloudinit",
		Cache: v1.CacheWriteThrough,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: "virtio",
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: "cloudinit",
		VolumeSource: v1.VolumeSource{
			CloudInitNoCloud: &v1.CloudInitNoCloudSource{
				UserDataBase64:    base64.StdEncoding.EncodeToString([]byte(userData)),
				NetworkDataBase64: base64.StdEncoding.EncodeToString([]byte(networkData)),
			},
		},
	})
}
