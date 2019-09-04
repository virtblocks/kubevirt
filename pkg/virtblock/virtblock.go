package virtblock

import (
	"fmt"

	"github.com/virtblocks/virtblocks/go/rust/pkg/devices"
)

func CreateMemBalloon() error {
	balloon := devices.NewMemballoon()
	balloon.SetModel(devices.MemballoonModelVirtio)
	fmt.Println(balloon.String())
}
