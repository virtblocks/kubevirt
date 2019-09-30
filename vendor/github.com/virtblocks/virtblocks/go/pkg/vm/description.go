// Copyright (C) 2019 Red Hat, Inc.
// SPDX-License-Identifier: LGPL-2.1-or-later

package vm

import (
	"errors"
	"github.com/virtblocks/virtblocks/go/pkg/devices"
	"strconv"
)

type Description struct {
	emulator string
	cpus     uint
	memory   uint
	disk     *devices.Disk
	serial   *devices.Serial
}

func NewDescription() *Description {
	return &Description{
		emulator: "/usr/bin/qemu-system-x86_64",
	}
}

func (self *Description) SetEmulator(emulator string) *Description {
	self.emulator = emulator
	return self
}

func (self *Description) SetCpus(cpus uint) *Description {
	self.cpus = cpus
	return self
}

func (self *Description) SetMemory(memory uint) *Description {
	self.memory = memory
	return self
}

func (self *Description) SetDisk(disk *devices.Disk) *Description {
	self.disk = disk
	return self
}

func (self *Description) SetSerial(serial *devices.Serial) *Description {
	self.serial = serial
	return self
}

func (self *Description) QemuCommandLine() ([]string, error) {
	var ret = []string{
		self.emulator,
		"-nodefaults",
		"-display",
		"none",
		"-smp",
		strconv.FormatUint(uint64(self.cpus), 10),
		"-m",
		strconv.FormatUint(uint64(self.memory), 10),
	}

	if self.cpus == 0 {
		return ret, errors.New("number of CPUs not set")
	}

	if self.memory == 0 {
		return ret, errors.New("memory size not set")
	}

	disk, err := self.disk.QemuCommandLine()
	if err != nil {
		return ret, err
	}

	ret = append(ret, disk...)

	serial, err := self.serial.QemuCommandLine()
	if err != nil {
		return ret, err
	}

	ret = append(ret, serial...)

	return ret, nil
}
