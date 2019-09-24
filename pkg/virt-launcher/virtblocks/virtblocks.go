package virtwrap

import "kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"

type VirtBlockDomain struct {
}

func (domain *VirtBlockDomain) Destroy() error {
	return nil
}

func (domain *VirtBlockDomain) Shutdown() error {
	return nil
}

func (domain *VirtBlockDomain) IsAlive() (bool, error) {
	return false, nil
}

func (domain *VirtBlockDomain) IsPaused() (bool, error) {
	return false, nil
}

func (domain *VirtBlockDomain) Spec() (*api.Domain, error) {
	return nil, nil
}

func (domain *VirtBlockDomain) Create(*api.Domain) error {
	return nil
}

func (domain *VirtBlockDomain) Resume() error {
	return nil
}

type VirtBlocks struct {
}

func (virtBlocks *VirtBlocks) GetDomain() (dom *VirtBlockDomain, err error) {
	return nil, nil
}

func isNotFound(error) bool {
	return false
}
