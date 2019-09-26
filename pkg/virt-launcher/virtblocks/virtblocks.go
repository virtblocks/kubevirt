package virtblocks

//go:generate mockgen -source $GOFILE -package=$GOPACKAGE -destination=generated_mock_$GOFILE

import "kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"

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
}

type VirtBlockDomainImpl struct {
}

func (domain *VirtBlockDomainImpl) Destroy() error {
	return nil
}

func (domain *VirtBlockDomainImpl) Shutdown() error {
	return nil
}

func (domain *VirtBlockDomainImpl) IsAlive() (bool, error) {
	return false, nil
}

func (domain *VirtBlockDomainImpl) IsPaused() (bool, error) {
	return false, nil
}

func (domain *VirtBlockDomainImpl) Spec() (*api.Domain, error) {
	return nil, nil
}

func (domain *VirtBlockDomainImpl) Create(*api.Domain) error {
	return nil
}

func (domain *VirtBlockDomainImpl) Resume() error {
	return nil
}

func (domain *VirtBlockDomainImpl) GetState() (api.LifeCycle, api.StateChangeReason, error) {
	return "", "", nil
}

type VirtBlocksImpl struct {
}

func (virtBlocks *VirtBlocksImpl) GetDomain() (dom VirtBlockDomain, err error) {
	return nil, nil
}

func isNotFound(error) bool {
	return false
}
