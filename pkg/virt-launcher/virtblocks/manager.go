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
 * Copyright 2017, 2018 Red Hat, Inc.
 *
 */

package virtblocks

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	cmdclient "kubevirt.io/kubevirt/pkg/virt-handler/cmd-client"
	eventsclient "kubevirt.io/kubevirt/pkg/virt-launcher/notify-client"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/log"
	cloudinit "kubevirt.io/kubevirt/pkg/cloud-init"
	"kubevirt.io/kubevirt/pkg/config"
	containerdisk "kubevirt.io/kubevirt/pkg/container-disk"
	"kubevirt.io/kubevirt/pkg/emptydisk"
	ephemeraldisk "kubevirt.io/kubevirt/pkg/ephemeral-disk"
	cmdv1 "kubevirt.io/kubevirt/pkg/handler-launcher-com/cmd/v1"
	"kubevirt.io/kubevirt/pkg/hooks"
	hostdisk "kubevirt.io/kubevirt/pkg/host-disk"
	"kubevirt.io/kubevirt/pkg/ignition"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/cli"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/network"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/stats"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/util"
)

const gpuEnvPrefix = "GPU_PASSTHROUGH_DEVICES"
const vgpuEnvPrefix = "VGPU_PASSTHROUGH_DEVICES"

type VirtBlocksDomainManager struct {
	virtBlocks       VirtBlocks
	kubevirtMetadata api.KubeVirtMetadata
	vmi              *v1.VirtualMachineInstance

	// Anytime a get and a set is done on the domain, this lock must be held.
	domainModifyLock sync.Mutex

	virtShareDir           string
	notifier               *eventsclient.Notifier
	lessPVCSpaceToleration int
}

type migrationDisks struct {
	shared    map[string]bool
	generated map[string]bool
}

func NewVirtBlocksDomainManager(virtBlocks VirtBlocks, virtShareDir string, notifier *eventsclient.Notifier, lessPVCSpaceToleration int) (virtwrap.DomainManager, error) {
	manager := VirtBlocksDomainManager{
		virtShareDir:           virtShareDir,
		notifier:               notifier,
		lessPVCSpaceToleration: lessPVCSpaceToleration,
		virtBlocks:             virtBlocks,
		kubevirtMetadata:       api.KubeVirtMetadata{},
	}

	return &manager, nil
}

func (d *migrationDisks) isSharedVolume(name string) bool {
	_, shared := d.shared[name]
	return shared
}

func (d *migrationDisks) isGeneratedVolume(name string) bool {
	_, generated := d.generated[name]
	return generated
}

func classifyVolumesForMigration(vmi *v1.VirtualMachineInstance) *migrationDisks {
	// This method collects all VMI volumes that should not be copied during
	// live migration. It also collects all generated disks suck as cloudinit, secrets, ServiceAccount and ConfigMaps
	// to make sure that these are being copied during migration.
	// Persistent volume claims without ReadWriteMany access mode
	// should be filtered out earlier in the process

	disks := &migrationDisks{
		shared:    make(map[string]bool),
		generated: make(map[string]bool),
	}
	for _, volume := range vmi.Spec.Volumes {
		volSrc := volume.VolumeSource
		if volSrc.PersistentVolumeClaim != nil || volSrc.DataVolume != nil ||
			(volSrc.HostDisk != nil && *volSrc.HostDisk.Shared) {
			disks.shared[volume.Name] = true
		}
		if volSrc.ConfigMap != nil || volSrc.Secret != nil ||
			volSrc.ServiceAccount != nil || volSrc.CloudInitNoCloud != nil ||
			volSrc.CloudInitConfigDrive != nil || volSrc.ContainerDisk != nil {
			disks.generated[volume.Name] = true
		}
	}
	return disks
}

func getAllDomainDisks(dom cli.VirDomain) ([]api.Disk, error) {
	xmlstr, err := dom.GetXMLDesc(0)
	if err != nil {
		return nil, err
	}
	var newSpec api.DomainSpec
	err = xml.Unmarshal([]byte(xmlstr), &newSpec)
	if err != nil {
		return nil, err
	}

	return newSpec.Devices.Disks, nil
}

func getDiskTargetsForMigration(dom cli.VirDomain, vmi *v1.VirtualMachineInstance) []string {
	// This method collects all VMI disks that needs to be copied during live migration
	// and returns a list of its target device names.
	// Shared volues are being excluded.
	copyDisks := []string{}
	migrationVols := classifyVolumesForMigration(vmi)
	disks, err := getAllDomainDisks(dom)
	if err != nil {
		log.Log.Object(vmi).Reason(err).Error("failed to parse domain XML to get disks.")
	}
	// the name of the volume should match the alias
	for _, disk := range disks {
		if disk.ReadOnly != nil && !migrationVols.isGeneratedVolume(disk.Alias.Name) {
			continue
		}
		if (disk.Type != "file" && disk.Type != "block") || migrationVols.isSharedVolume(disk.Alias.Name) {
			continue
		}
		copyDisks = append(copyDisks, disk.Target.Device)
	}
	return copyDisks
}

func getVMIEphemeralDisksTotalSize() *resource.Quantity {
	var baseDir = "/var/run/kubevirt-ephemeral-disks/"
	totalSize := int64(0)
	err := filepath.Walk(baseDir, func(path string, f os.FileInfo, err error) error {
		if !f.IsDir() {
			totalSize += f.Size()
		}
		return err
	})
	if err != nil {
		log.Log.Reason(err).Warning("failed to get VMI ephemeral disks size")
		return &resource.Quantity{Format: resource.BinarySI}
	}

	return resource.NewScaledQuantity(totalSize, 0)
}

func (l *VirtBlocksDomainManager) CancelVMIMigration(vmi *v1.VirtualMachineInstance) error {
	return fmt.Errorf("not implemented")
}

func (l *VirtBlocksDomainManager) MigrateVMI(vmi *v1.VirtualMachineInstance, options *cmdclient.MigrationOptions) error {
	return fmt.Errorf("not implemented")
}

var updateHostsFile = func(entry string) error {
	file, err := os.OpenFile("/etc/hosts", os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed opening file: %s", err)
	}
	defer file.Close()

	_, err = file.WriteString(entry)
	if err != nil {
		return fmt.Errorf("failed writing to file: %s", err)
	}
	return nil
}

// Prepares the target pod environment by executing the preStartHook
func (l *VirtBlocksDomainManager) PrepareMigrationTarget(vmi *v1.VirtualMachineInstance, useEmulation bool) error {
	return fmt.Errorf("not implemented")
}

// All local environment setup that needs to occur before VirtualMachineInstance starts
// can be done in this function. This includes things like...
//
// - storage prep
// - network prep
// - cloud-init
//
// The Domain.Spec can be alterned in this function and any changes
// made to the domain will get set in libvirt after this function exits.
func (l *VirtBlocksDomainManager) preStartHook(vmi *v1.VirtualMachineInstance, domain *api.Domain) (*api.Domain, error) {

	l.kubevirtMetadata = domain.Spec.Metadata.KubeVirt
	logger := log.Log.Object(vmi)

	logger.Info("Executing PreStartHook on VMI pod environment")

	// generate cloud-init data
	cloudInitData, err := cloudinit.ReadCloudInitVolumeDataSource(vmi)
	if err != nil {
		return domain, fmt.Errorf("PreCloudInitIso hook failed: %v", err)
	}

	// Pass cloud-init data to PreCloudInitIso hook
	logger.Info("Starting PreCloudInitIso hook")
	hooksManager := hooks.GetManager()
	cloudInitData, err = hooksManager.PreCloudInitIso(vmi, cloudInitData)
	if err != nil {
		return domain, fmt.Errorf("PreCloudInitIso hook failed: %v", err)
	}

	if cloudInitData != nil {
		err := cloudinit.GenerateLocalData(vmi.Name, vmi.Namespace, cloudInitData)
		if err != nil {
			return domain, fmt.Errorf("generating local cloud-init data failed: %v", err)
		}
	}

	// generate ignition data
	ignitionData := ignition.GetIgnitionSource(vmi)
	if ignitionData != "" {

		err := ignition.GenerateIgnitionLocalData(vmi, vmi.Namespace)
		if err != nil {
			return domain, err
		}
	}

	// setup networking
	err = network.SetupPodNetwork(vmi, domain)
	if err != nil {
		return domain, fmt.Errorf("preparing the pod network failed: %v", err)
	}

	// create disks images on the cluster lever
	// or initalize disks images for empty PVC
	hostDiskCreator := hostdisk.NewHostDiskCreator(l.notifier, l.lessPVCSpaceToleration)
	err = hostDiskCreator.Create(vmi)
	if err != nil {
		return domain, fmt.Errorf("preparing host-disks failed: %v", err)
	}

	// Create ephemeral disk for container disks
	err = containerdisk.CreateEphemeralImages(vmi)
	if err != nil {
		return domain, fmt.Errorf("preparing ephemeral container disk images failed: %v", err)
	}
	// Create images for volumes that are marked ephemeral.
	err = ephemeraldisk.CreateEphemeralImages(vmi)
	if err != nil {
		return domain, fmt.Errorf("preparing ephemeral images failed: %v", err)
	}
	// create empty disks if they exist
	if err := emptydisk.CreateTemporaryDisks(vmi); err != nil {
		return domain, fmt.Errorf("creating empty disks failed: %v", err)
	}
	// create ConfigMap disks if they exists
	if err := config.CreateConfigMapDisks(vmi); err != nil {
		return domain, fmt.Errorf("creating config map disks failed: %v", err)
	}
	// create Secret disks if they exists
	if err := config.CreateSecretDisks(vmi); err != nil {
		return domain, fmt.Errorf("creating secret disks failed: %v", err)
	}
	// create ServiceAccount disk if exists
	if err := config.CreateServiceAccountDisk(vmi); err != nil {
		return domain, fmt.Errorf("creating service account disk failed: %v", err)
	}

	// set drivers cache mode
	for i := range domain.Spec.Devices.Disks {
		err := api.SetDriverCacheMode(&domain.Spec.Devices.Disks[i])
		if err != nil {
			return domain, err
		}
	}

	return domain, err
}

// This function parses variables that are set by SR-IOV device plugin listing
// PCI IDs for devices allocated to the pod. It also parses variables that
// virt-controller sets mapping network names to their respective resource
// names (if any).
//
// Format for PCI ID variables set by SR-IOV DP is:
// "": for no allocated devices
// PCIDEVICE_<resourceName>="0000:81:11.1": for a single device
// PCIDEVICE_<resourceName>="0000:81:11.1 0000:81:11.2[ ...]": for multiple devices
//
// Since special characters in environment variable names are not allowed,
// resourceName is mutated as follows:
// 1. All dots and slashes are replaced with underscore characters.
// 2. The result is upper cased.
//
// Example: PCIDEVICE_INTEL_COM_SRIOV_TEST=... for intel.com/sriov_test resources.
//
// Format for network to resource mapping variables is:
// KUBEVIRT_RESOURCE_NAME_<networkName>=<resourceName>
//
func resourceNameToEnvvar(resourceName string) string {
	varName := strings.ToUpper(resourceName)
	varName = strings.Replace(varName, "/", "_", -1)
	varName = strings.Replace(varName, ".", "_", -1)
	return fmt.Sprintf("PCIDEVICE_%s", varName)
}

func getSRIOVPCIAddresses(ifaces []v1.Interface) map[string][]string {
	networkToAddressesMap := map[string][]string{}
	for _, iface := range ifaces {
		if iface.SRIOV == nil {
			continue
		}
		networkToAddressesMap[iface.Name] = []string{}
		varName := fmt.Sprintf("KUBEVIRT_RESOURCE_NAME_%s", iface.Name)
		resourceName, isSet := os.LookupEnv(varName)
		if isSet {
			varName := resourceNameToEnvvar(resourceName)
			pciAddrString, isSet := os.LookupEnv(varName)
			if isSet {
				addrs := strings.Split(pciAddrString, ",")
				naddrs := len(addrs)
				if naddrs > 0 {
					if addrs[naddrs-1] == "" {
						addrs = addrs[:naddrs-1]
					}
				}
				networkToAddressesMap[iface.Name] = addrs
			} else {
				log.DefaultLogger().Warningf("%s not set for SR-IOV interface %s", varName, iface.Name)
			}
		} else {
			log.DefaultLogger().Warningf("%s not set for SR-IOV interface %s", varName, iface.Name)
		}
	}
	return networkToAddressesMap
}

// This function parses all environment variables with prefix string that is set by a Device Plugin.
// Device plugin that passes GPU devices by setting these env variables is https://github.com/NVIDIA/kubevirt-gpu-device-plugin
// It returns address list for devices set in the env variable.
// The format is as follows:
// "":for no address set
// "<address_1>,": for a single address
// "<address_1>,<address_2>[,...]": for multiple addresses
func getEnvAddressListByPrefix(evnPrefix string) []string {
	var returnAddr []string
	for _, env := range os.Environ() {
		split := strings.Split(env, "=")
		if strings.HasPrefix(split[0], evnPrefix) {
			returnAddr = append(returnAddr, parseDeviceAddress(split[1])...)
		}
	}
	return returnAddr
}

func parseDeviceAddress(addrString string) []string {
	addrs := strings.Split(addrString, ",")
	naddrs := len(addrs)
	if naddrs > 0 {
		if addrs[naddrs-1] == "" {
			addrs = addrs[:naddrs-1]
		}
	}

	for index, element := range addrs {
		addrs[index] = strings.TrimSpace(element)
	}
	return addrs
}
func (l *VirtBlocksDomainManager) SyncVMI(vmi *v1.VirtualMachineInstance, useEmulation bool, options *cmdv1.VirtualMachineOptions) (*api.DomainSpec, error) {
	l.domainModifyLock.Lock()
	defer l.domainModifyLock.Unlock()
	l.vmi = vmi

	logger := log.Log.Object(vmi)

	domain := &api.Domain{}
	podCPUSet, err := util.GetPodCPUSet()
	if err != nil {
		logger.Reason(err).Error("failed to read pod cpuset.")
		return nil, err
	}

	// Check if PVC volumes are block volumes
	isBlockPVCMap := make(map[string]bool)
	isBlockDVMap := make(map[string]bool)
	diskInfo := make(map[string]*containerdisk.DiskInfo)
	for i, volume := range vmi.Spec.Volumes {
		if volume.VolumeSource.PersistentVolumeClaim != nil {
			isBlockPVC, err := isBlockDeviceVolume(volume.Name)
			if err != nil {
				logger.Reason(err).Errorf("failed to detect volume mode for Volume %v and PVC %v.",
					volume.Name, volume.VolumeSource.PersistentVolumeClaim.ClaimName)
				return nil, err
			}
			isBlockPVCMap[volume.Name] = isBlockPVC
		} else if volume.VolumeSource.ContainerDisk != nil {
			image, err := containerdisk.GetDiskTargetPartFromLauncherView(i)
			if err != nil {
				return nil, err
			}
			info, err := GetImageInfo(image)
			if err != nil {
				return nil, err
			}
			diskInfo[volume.Name] = info
		} else if volume.VolumeSource.DataVolume != nil {
			isBlockDV, err := isBlockDeviceVolume(volume.Name)
			if err != nil {
				logger.Reason(err).Errorf("failed to detect volume mode for Volume %v and DV %v.",
					volume.Name, volume.VolumeSource.DataVolume.Name)
				return nil, err
			}
			isBlockDVMap[volume.Name] = isBlockDV
		}
	}

	// Map the VirtualMachineInstance to the Domain
	c := &api.ConverterContext{
		VirtualMachine: vmi,
		UseEmulation:   useEmulation,
		CPUSet:         podCPUSet,
		IsBlockPVC:     isBlockPVCMap,
		IsBlockDV:      isBlockDVMap,
		DiskType:       diskInfo,
		SRIOVDevices:   getSRIOVPCIAddresses(vmi.Spec.Domain.Devices.Interfaces),
		GpuDevices:     getEnvAddressListByPrefix(gpuEnvPrefix),
		VgpuDevices:    getEnvAddressListByPrefix(vgpuEnvPrefix),
	}
	if options != nil && options.VirtualMachineSMBios != nil {
		c.SMBios = options.VirtualMachineSMBios
	}
	if err := api.Convert_v1_VirtualMachine_To_api_Domain(vmi, domain, c); err != nil {
		logger.Error("Conversion failed.")
		return nil, err
	}

	// Set defaults which are not coming from the cluster
	api.SetObjectDefaults_Domain(domain)

	dom, err := l.virtBlocks.GetDomain()
	newDomain := false
	alive := false
	if err != nil {
		// We need the domain but it does not exist, so create it
		if isNotFound(err) {
			newDomain = true
			domain, err = l.preStartHook(vmi, domain)
			if err != nil {
				logger.Reason(err).Error("pre start setup for VirtualMachineInstance failed.")
				return nil, err
			}
			domain.Spec, err = l.setDomainSpecWithHooks(vmi, &domain.Spec)
			if err != nil {
				return nil, err
			}
			logger.Info("Domain defined.")

		} else {
			logger.Reason(err).Error("Getting the domain failed.")
			return nil, err
		}
	} else {
		alive, err = dom.IsAlive()
		if err != nil {
			return nil, err
		}
	}

	// To make sure, that we set the right qemu wrapper arguments,
	// we update the domain XML whenever a VirtualMachineInstance was already defined but not running
	if !newDomain && !alive {
		domain.Spec, err = l.setDomainSpecWithHooks(vmi, &domain.Spec)
		if err != nil {
			return nil, err
		}
	}

	// TODO Suspend, Pause, ..., for now we only support reaching the running state
	// TODO for migration and error detection we also need the state change reason
	// TODO blocked state
	if !alive && !vmi.IsRunning() && !vmi.IsFinal() {
		err = dom.Create(domain)
		if err != nil {
			logger.Reason(err).Error("Starting the VirtualMachineInstance failed.")
			return nil, err
		}
		logger.Info("Domain started.")
	} else if paused, err := dom.IsPaused(); err == nil && paused {
		// TODO: if state change reason indicates a system error, we could try something smarter
		err := dom.Resume()
		if err != nil {
			logger.Reason(err).Error("Resuming the VirtualMachineInstance failed.")
			return nil, err
		}
		logger.Info("Domain resumed.")
	} else if err != nil {
		return nil, err
	}

	newSpec, err := l.specWithMetadata(dom)
	if err != nil {
		return nil, err
	}

	// TODO: check if VirtualMachineInstance Spec and Domain Spec are equal or if we have to sync
	return &newSpec.Spec, nil
}

func isBlockDeviceVolume(volumeName string) (bool, error) {
	// check for block device
	path := api.GetBlockDeviceVolumePath(volumeName)
	fileInfo, err := os.Stat(path)
	if err == nil {
		if (fileInfo.Mode() & os.ModeDevice) != 0 {
			return true, nil
		}
		return false, fmt.Errorf("found %v, but it's not a block device", path)
	}
	if os.IsNotExist(err) {
		// cross check: is it a filesystem volume
		path = api.GetFilesystemVolumePath(volumeName)
		fileInfo, err := os.Stat(path)
		if err == nil {
			if fileInfo.Mode().IsRegular() {
				return false, nil
			}
			return false, fmt.Errorf("found %v, but it's not a regular file", path)
		}
		if os.IsNotExist(err) {
			return false, fmt.Errorf("neither found block device nor regular file for volume %v", volumeName)
		}
	}
	return false, fmt.Errorf("error checking for block device: %v", err)
}

func (l *VirtBlocksDomainManager) SignalShutdownVMI(vmi *v1.VirtualMachineInstance) error {
	l.domainModifyLock.Lock()
	defer l.domainModifyLock.Unlock()

	dom, err := l.virtBlocks.GetDomain()
	if err != nil {
		// If the VirtualMachineInstance does not exist, we are done
		if isNotFound(err) {
			return nil
		} else {
			log.Log.Object(vmi).Reason(err).Error("Getting the domain failed during graceful shutdown.")
			return err
		}
	}

	if alive, err := dom.IsAlive(); err == nil && alive {

		if l.kubevirtMetadata.GracePeriod.DeletionTimestamp == nil {
			err = dom.Shutdown()
			if err != nil {
				log.Log.Object(vmi).Reason(err).Error("Signalling graceful shutdown failed.")
				return err
			}
			log.Log.Object(vmi).Infof("Signaled graceful shutdown for %s", vmi.GetObjectMeta().GetName())

			now := metav1.Now()
			l.kubevirtMetadata.GracePeriod.DeletionTimestamp = &now
		}
	} else {
		return err
	}

	return nil
}

func (l *VirtBlocksDomainManager) KillVMI(vmi *v1.VirtualMachineInstance) error {
	dom, err := l.virtBlocks.GetDomain()
	if err != nil {
		// If the VirtualMachineInstance does not exist, we are done
		if isNotFound(err) {
			return nil
		} else {
			log.Log.Object(vmi).Reason(err).Error("Getting the domain failed.")
			return err
		}
	}

	if alive, err := dom.IsAlive(); err == nil && alive {
		err = dom.Destroy()
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			log.Log.Object(vmi).Reason(err).Error("Destroying the domain state failed.")
			return err
		}
		log.Log.Object(vmi).Info("Domain stopped.")
		return nil
	} else {
		return err
	}

	log.Log.Object(vmi).Info("Domain not running or paused, nothing to do.")
	return nil
}

func (l *VirtBlocksDomainManager) DeleteVMI(vmi *v1.VirtualMachineInstance) error {
	return nil
}

func (l *VirtBlocksDomainManager) ListAllDomains() ([]*api.Domain, error) {
	dom, err := l.virtBlocks.GetDomain()
	if err != nil {
		return nil, err
	}
	domainSpec, err := l.specWithMetadata(dom)
	if err != nil {
		if isNotFound(err) {
			return []*api.Domain{}, nil
		}
		return nil, err
	}
	return []*api.Domain{domainSpec}, nil
}

func (l *VirtBlocksDomainManager) specWithMetadata(dom VirtBlockDomain) (*api.Domain, error) {
	spec, err := dom.Spec()
	if err != nil {
		return nil, err
	}
	spec.Spec.Metadata.KubeVirt = l.kubevirtMetadata
	return spec, nil
}

func (l *VirtBlocksDomainManager) setDomainSpecWithHooks(vmi *v1.VirtualMachineInstance, spec *api.DomainSpec) (api.DomainSpec, error) {

	hooksManager := hooks.GetManager()
	domainSpec, err := hooksManager.OnDefineDomain(spec, vmi)
	if err != nil {
		return api.DomainSpec{}, err
	}
	spec = &api.DomainSpec{}
	err = xml.Unmarshal([]byte(domainSpec), spec)
	if err != nil {
		return api.DomainSpec{}, err
	}
	return *spec, nil
}

func (l *VirtBlocksDomainManager) GetDomainStats() ([]*stats.DomainStats, error) {
	return []*stats.DomainStats{}, nil
}

func GetImageInfo(imagePath string) (*containerdisk.DiskInfo, error) {

	out, err := exec.Command(
		"/usr/bin/qemu-img", "info", imagePath, "--output", "json",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("failed to invoke qemu-img: %v", err)
	}
	info := &containerdisk.DiskInfo{}
	err = json.Unmarshal(out, info)
	if err != nil {
		return nil, fmt.Errorf("failed to parse disk info: %v", err)
	}
	return info, err
}
