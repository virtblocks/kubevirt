// Virt Blocks
//
// Copyright (C) 2019 Red Hat, Inc.
//
// This software is distributed under the terms of the MIT License.
// See the LICENSE file in the top level directory for details.

package devices

// #cgo CPPFLAGS: -I../../../../c/rust/
// #cgo LDFLAGS: -L../../../../c/rust/.libs/ -lvirtblocks_c_rust -pthread -lm -ldl
// #include <stdlib.h>
// #include <virtblocks.h>
import "C"

import (
	"unsafe"
)

type MemballoonModel int

const (
	MemballoonModelNone                  = MemballoonModel(C.VIRTBLOCKS_DEVICES_MEMBALLOON_MODEL_NONE)
	MemballoonModelVirtio                = MemballoonModel(C.VIRTBLOCKS_DEVICES_MEMBALLOON_MODEL_VIRTIO)
	MemballoonModelVirtioNonTransitional = MemballoonModel(C.VIRTBLOCKS_DEVICES_MEMBALLOON_MODEL_VIRTIO_NON_TRANSITIONAL)
	MemballoonModelVirtioTransitional    = MemballoonModel(C.VIRTBLOCKS_DEVICES_MEMBALLOON_MODEL_VIRTIO_TRANSITIONAL)
)

type Memballoon struct {
	ptr *C.VirtBlocksDevicesMemballoon
}

func NewMemballoon() *Memballoon {
	return &Memballoon{ptr: C.virtblocks_devices_memballoon_new()}
}

func (self *Memballoon) Free() {
	C.virtblocks_devices_memballoon_free(self.ptr)
}

func (self *Memballoon) SetModel(model MemballoonModel) {
	C.virtblocks_devices_memballoon_set_model(self.ptr, C.VirtBlocksDevicesMemballoonModel(model))
}

func (self Memballoon) Model() MemballoonModel {
	return MemballoonModel(C.virtblocks_devices_memballoon_get_model(self.ptr))
}

func (self Memballoon) String() string {
	var c_str = C.virtblocks_devices_memballoon_to_string(self.ptr)
	defer C.free(unsafe.Pointer(c_str))
	return C.GoString(c_str)
}
