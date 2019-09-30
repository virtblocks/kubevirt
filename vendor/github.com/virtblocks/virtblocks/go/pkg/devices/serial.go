// Copyright (C) 2019 Red Hat, Inc.
// SPDX-License-Identifier: LGPL-2.1-or-later

package devices

import (
	"errors"
	"fmt"
)

type Serial struct {
	path string
}

func NewSerial() *Serial {
	return &Serial{}
}

func (self *Serial) SetPath(path string) *Serial {
	self.path = path
	return self
}

func (self *Serial) QemuCommandLine() ([]string, error) {
	var ret = make([]string, 0)

	if self.path == "" {
		return ret, errors.New("path not set")
	}

	ret = append(ret,
		"-chardev",
		fmt.Sprintf("socket,path=%s,server,nowait,id=serial0", self.path),
		"-device",
		"isa-serial,chardev=serial0",
	)

	return ret, nil
}
