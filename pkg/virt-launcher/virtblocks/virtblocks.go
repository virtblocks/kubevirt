package virtblocks

//go:generate mockgen -source $GOFILE -package=$GOPACKAGE -destination=generated_mock_$GOFILE

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/libvirt/libvirt-go"
	"github.com/virtblocks/virtblocks/go/pkg/devices"
	"github.com/virtblocks/virtblocks/go/pkg/vm"

	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/errors"
)

type VirtBlocks interface {
	GetDomain() (dom VirtBlockDomain, err error)
}

type VirtBlockDomain interface {
	Destroy() error
	Shutdown() error
	IsAlive() (bool, error)
	IsPaused() (bool, error)
	Spec() (*api.Domain, error)
	Create(*api.Domain) error
	Resume() error
	GetState() (api.LifeCycle, api.StateChangeReason, error)
	Name() string
}

type VirtBlockDomainImpl struct {
	lock sync.Mutex
	cmd  *exec.Cmd
	dom  *api.Domain
	name string
}

func (domain *VirtBlockDomainImpl) Name() string {
	return domain.name
}

func (domain *VirtBlockDomainImpl) Destroy() error {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	if domain.cmd == nil {
		return nil
	}
	err := domain.cmd.Process.Kill()
	if err != nil && !strings.Contains(err.Error(), "process already finished") {
		return fmt.Errorf("failed to stop process %v: %v", domain.cmd.Process.Pid, err)
	}
	return nil
}

func (domain *VirtBlockDomainImpl) Shutdown() error {
	return domain.Destroy()
}

func (domain *VirtBlockDomainImpl) IsAlive() (bool, error) {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	return domain.isAlive()
}

func (domain *VirtBlockDomainImpl) isAlive() (bool, error) {
	if domain.cmd == nil {
		return false, nil
	}
	err := domain.cmd.Process.Signal(syscall.Signal(0))
	if err != nil && strings.Contains(err.Error(), "process already finished") {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to determine if process exists: %v", err)
	} else {
		return true, nil
	}
}

func (domain *VirtBlockDomainImpl) IsPaused() (bool, error) {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	return false, nil
}

func (domain *VirtBlockDomainImpl) Spec() (*api.Domain, error) {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	return domain.dom, nil
}

func (domain *VirtBlockDomainImpl) Create(dom *api.Domain) error {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	disk := devices.NewDisk()
	serial := devices.NewSerial()
	serial.SetPath(dom.Spec.Devices.Serials[0].Source.Path)
	disk.SetFilename(dom.Spec.Devices.Disks[0].Source.File)
	cmdStr, err := vm.NewDescription().SetEmulator("qemu-system-x86_64").SetCpus(1).SetMemory(64).SetDisk(disk).SetSerial(serial).QemuCommandLine()
	if err != nil {
		return fmt.Errorf("failed to start qemu domain: %v: %v", err, cmdStr)
	}
	if alive, err := domain.isAlive(); err == nil && alive {
		return fmt.Errorf("qemu process already running")
	} else if err != nil {
		return fmt.Errorf("could not determine if process is already running: %v", err)
	}
	cmd := exec.Command(cmdStr[0], cmdStr[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start qemu process: %v", err)
	}
	domain.cmd = cmd
	domain.dom = dom
	return nil
}

func (domain *VirtBlockDomainImpl) Resume() error {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	return nil
}

func (domain *VirtBlockDomainImpl) GetState() (api.LifeCycle, api.StateChangeReason, error) {
	domain.lock.Lock()
	defer domain.lock.Unlock()
	if alive, err := domain.isAlive(); alive && err == nil {
		return api.Running, api.ReasonUser, nil
	} else if err != nil {
		return api.NoState, api.ReasonNonExistent, err
	}
	if domain.cmd == nil {
		return api.NoState, api.ReasonNonExistent, nil
	}
	return api.Shutdown, api.ReasonUser, nil
}

type VirtBlocksImpl struct {
	Name      string
	UID       string
	Namespace string
	dom       VirtBlockDomain
}

func NewVirtBlocks(name string, uid string, namespace string) *VirtBlocksImpl {
	return &VirtBlocksImpl{
		Name:      name,
		UID:       uid,
		Namespace: namespace,
		dom:       &VirtBlockDomainImpl{name: name},
	}
}

func (virtBlocks *VirtBlocksImpl) GetDomain() (VirtBlockDomain, error) {
	if alive, err := virtBlocks.dom.IsAlive(); alive && err == nil {
		return virtBlocks.dom, nil
	} else if err != nil {
		return nil, err
	}
	return virtBlocks.dom, libvirt.Error{Code: libvirt.ERR_NO_DOMAIN}
}

func isNotFound(err error) bool {
	return errors.IsNotFound(err)
}
