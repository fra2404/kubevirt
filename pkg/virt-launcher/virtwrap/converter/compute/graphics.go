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
 * Copyright The KubeVirt Authors.
 *
 */

package compute

import (
	"fmt"
	"strings"

	v1 "kubevirt.io/api/core/v1"

	"kubevirt.io/kubevirt/pkg/pointer"

	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
)

const (
	graphicsDeviceDefaultHeads uint = 1
	graphicsDeviceDefaultVRAM  uint = 16384
)

type GraphicsDomainConfigurator struct {
	architecture         string
	useBochsForEFIGuests bool
}

func NewGraphicsDomainConfigurator(architecture string, useBochsForEFIGuests bool) GraphicsDomainConfigurator {
	return GraphicsDomainConfigurator{
		architecture:         architecture,
		useBochsForEFIGuests: useBochsForEFIGuests,
	}
}

func (g GraphicsDomainConfigurator) Configure(vmi *v1.VirtualMachineInstance, domain *api.Domain) error {
	if vmi.Spec.Domain.Devices.AutoattachGraphicsDevice != nil && !*vmi.Spec.Domain.Devices.AutoattachGraphicsDevice {
		return nil
	}

	domain.Spec.Devices.Graphics = []api.Graphics{
		{
			Listen: &api.GraphicsListen{
				Type:   "socket",
				Socket: fmt.Sprintf("/var/run/kubevirt-private/%s/virt-vnc", vmi.ObjectMeta.UID),
			},
			Type: "vnc",
		},
	}

	// we need to use QEMUArgs as libvirt doesn't support the VNC server listening on both TCP/Websocket and UNIX sockets
	if vmi.Spec.Domain.Devices.QEMUVNCServer != nil {
		var vncOptions []string
		initializeQEMUCmdAndQEMUArg(domain)
		if vmi.Spec.Domain.Devices.QEMUVNCServer.EnableTCP {
			vncOptions = append(vncOptions, "0.0.0.0:0")
		}
		if vmi.Spec.Domain.Devices.QEMUVNCServer.EnableWS {
			vncOptions = append(vncOptions, "websocket=5901")
		}
		if len(vncOptions) != 0 {
			domain.Spec.QEMUCmd.QEMUArg = append(domain.Spec.QEMUCmd.QEMUArg,
				api.Arg{Value: "-vnc"},
				api.Arg{Value: strings.Join(vncOptions, ",")},
			)
		}
	}

	g.configureVideoDevice(vmi, domain)

	return nil
}

func (g GraphicsDomainConfigurator) configureVideoDevice(vmi *v1.VirtualMachineInstance, domain *api.Domain) {
	if vmi.Spec.Domain.Devices.Video != nil {
		video := api.Video{
			Model: api.VideoModel{
				Type:  vmi.Spec.Domain.Devices.Video.Type,
				VRam:  pointer.P(graphicsDeviceDefaultVRAM),
				Heads: pointer.P(graphicsDeviceDefaultHeads),
			},
		}
		domain.Spec.Devices.Video = []api.Video{video}
		return
	}

	switch g.architecture {
	case "amd64":
		g.configureAMD64VideoDevice(vmi, domain)
	case "arm64":
		g.configureARM64VideoDevice(domain)
	case "s390x":
		g.configureS390XVideoDevice(domain)
	}
}

func (g GraphicsDomainConfigurator) configureAMD64VideoDevice(vmi *v1.VirtualMachineInstance, domain *api.Domain) {
	// For AMD64 + EFI, use bochs. For BIOS, use VGA
	if g.useBochsForEFIGuests && vmi.IsBootloaderEFI() {
		domain.Spec.Devices.Video = []api.Video{
			{
				Model: api.VideoModel{
					Type:  "bochs",
					Heads: pointer.P(graphicsDeviceDefaultHeads),
				},
			},
		}
	} else {
		domain.Spec.Devices.Video = []api.Video{
			{
				Model: api.VideoModel{
					Type:  "vga",
					Heads: pointer.P(graphicsDeviceDefaultHeads),
					VRam:  pointer.P(graphicsDeviceDefaultVRAM),
				},
			},
		}
	}
}

func (g GraphicsDomainConfigurator) configureARM64VideoDevice(domain *api.Domain) {
	// For arm64, qemu-kvm only support virtio-gpu display device, so set it as default video device.
	domain.Spec.Devices.Video = []api.Video{
		{
			Model: api.VideoModel{
				Type:  v1.VirtIO,
				Heads: pointer.P(graphicsDeviceDefaultHeads),
			},
		},
	}
}

func (g GraphicsDomainConfigurator) configureS390XVideoDevice(domain *api.Domain) {
	domain.Spec.Devices.Video = []api.Video{{
		Model: api.VideoModel{
			Type:  v1.VirtIO,
			Heads: pointer.P(graphicsDeviceDefaultHeads),
		},
	}}
}
