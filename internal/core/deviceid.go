package core

import (
	"crypto/tls"

	"github.com/syncthing/syncthing/lib/protocol"
)

func syncthingDeviceID(cert tls.Certificate) protocol.DeviceID {
	return protocol.NewDeviceID(cert.Certificate[0])
}
