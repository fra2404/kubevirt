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

package admitter

import (
	"fmt"
	"net"
	"regexp"

	"kubevirt.io/kubevirt/pkg/network/link"
	"kubevirt.io/kubevirt/pkg/network/vmispec"
	hwutil "kubevirt.io/kubevirt/pkg/util/hardware"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	k8sfield "k8s.io/apimachinery/pkg/util/validation/field"

	v1 "kubevirt.io/api/core/v1"
)

func validateNetworksAssignedToInterfaces(field *k8sfield.Path, spec *v1.VirtualMachineInstanceSpec) []metav1.StatusCause {
	var causes []metav1.StatusCause
	const nameOfTypeNotFoundMessagePattern = "%s '%s' not found."
	interfaceSet := vmispec.IndexInterfaceSpecByName(spec.Domain.Devices.Interfaces)
	for i, network := range spec.Networks {
		if _, exists := interfaceSet[network.Name]; !exists {
			causes = append(causes, metav1.StatusCause{
				Type:    metav1.CauseTypeFieldValueRequired,
				Message: fmt.Sprintf(nameOfTypeNotFoundMessagePattern, field.Child("networks").Index(i).Child("name").String(), network.Name),
				Field:   field.Child("networks").Index(i).Child("name").String(),
			})
		}
	}
	return causes
}

func validateInterfacesAssignedToNetworks(field *k8sfield.Path, spec *v1.VirtualMachineInstanceSpec) []metav1.StatusCause {
	var causes []metav1.StatusCause
	const nameOfTypeNotFoundMessagePattern = "%s '%s' not found."
	networkSet := vmispec.IndexNetworkSpecByName(spec.Networks)
	for idx, iface := range spec.Domain.Devices.Interfaces {
		if _, exists := networkSet[iface.Name]; !exists {
			causes = append(causes, metav1.StatusCause{
				Type: metav1.CauseTypeFieldValueInvalid,
				Message: fmt.Sprintf(
					nameOfTypeNotFoundMessagePattern,
					field.Child("domain", "devices", "interfaces").Index(idx).Child("name").String(),
					iface.Name,
				),
				Field: field.Child("domain", "devices", "interfaces").Index(idx).Child("name").String(),
			})
		}
	}
	return causes
}

func validateNetworkNameUnique(field *k8sfield.Path, spec *v1.VirtualMachineInstanceSpec) []metav1.StatusCause {
	var causes []metav1.StatusCause
	networkSet := map[string]struct{}{}
	for i, network := range spec.Networks {
		if _, exists := networkSet[network.Name]; exists {
			causes = append(causes, metav1.StatusCause{
				Type:    metav1.CauseTypeFieldValueDuplicate,
				Message: fmt.Sprintf("Network with name %q already exists, every network must have a unique name", network.Name),
				Field:   field.Child("networks").Index(i).Child("name").String(),
			})
		}
		networkSet[network.Name] = struct{}{}
	}
	return causes
}

func validateInterfaceNameUnique(field *k8sfield.Path, spec *v1.VirtualMachineInstanceSpec) []metav1.StatusCause {
	var causes []metav1.StatusCause
	ifaceSet := map[string]struct{}{}
	for idx, iface := range spec.Domain.Devices.Interfaces {
		if _, exists := ifaceSet[iface.Name]; exists {
			causes = append(causes, metav1.StatusCause{
				Type:    metav1.CauseTypeFieldValueDuplicate,
				Message: "Only one interface can be connected to one specific network",
				Field:   field.Child("domain", "devices", "interfaces").Index(idx).Child("name").String(),
			})
		}
		ifaceSet[iface.Name] = struct{}{}
	}
	return causes
}

func validateInterfacesFields(field *k8sfield.Path, spec *v1.VirtualMachineInstanceSpec) []metav1.StatusCause {
	var causes []metav1.StatusCause
	networksByName := vmispec.IndexNetworkSpecByName(spec.Networks)
	for idx, iface := range spec.Domain.Devices.Interfaces {
		var interfaceField = field.Child("domain", "devices", "interfaces").Index(idx)
		causes = append(causes, validateInterfaceNameFormat(interfaceField, iface)...)
		causes = append(causes, validateInterfaceModel(interfaceField, iface)...)
		causes = append(causes, validateMacAddress(interfaceField, iface)...)
		causes = append(causes, validatePciAddress(interfaceField, iface)...)
		causes = append(causes, validatePortConfiguration(interfaceField, iface, networksByName[iface.Name])...)
		causes = append(causes, validateDHCPOptions(interfaceField, iface)...)
	}
	return causes
}

func validateInterfaceNameFormat(interfaceField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	isValid := regexp.MustCompile(`^[A-Za-z0-9-_]+$`).MatchString
	if !isValid(iface.Name) {
		return []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Message: "Network interface name can only contain alphabetical characters, numbers, dashes (-) or underscores (_)",
			Field:   interfaceField.Child("name").String(),
		}}
	}
	return nil
}

var validInterfaceModels = map[string]struct{}{
	"e1000":    {},
	"e1000e":   {},
	"igb":      {},
	"ne2k_pci": {},
	"pcnet":    {},
	"rtl8139":  {},
	v1.VirtIO:  {},
}

func validateInterfaceModel(interfaceField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	if iface.Model != "" {
		if _, exists := validInterfaceModels[iface.Model]; !exists {
			return []metav1.StatusCause{{
				Type: metav1.CauseTypeFieldValueNotSupported,
				Message: fmt.Sprintf(
					"interface %s uses model %s that is not supported.",
					interfaceField.Child("name").String(),
					iface.Model,
				),
				Field: interfaceField.Child("model").String(),
			}}
		}
	}
	return nil
}

func validateMacAddress(interfaceField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	var causes []metav1.StatusCause
	if err := link.ValidateMacAddress(iface.MacAddress); err != nil {
		causes = append(causes, metav1.StatusCause{
			Type: metav1.CauseTypeFieldValueInvalid,
			Message: fmt.Sprintf(
				"interface %s has %s.",
				interfaceField.Child("name").String(),
				err.Error(),
			),
			Field: interfaceField.Child("macAddress").String(),
		})
	}
	return causes
}

func validatePciAddress(interfaceField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	if iface.PciAddress != "" {
		_, err := hwutil.ParsePciAddress(iface.PciAddress)
		if err != nil {
			return []metav1.StatusCause{{
				Type: metav1.CauseTypeFieldValueInvalid,
				Message: fmt.Sprintf(
					"interface %s has malformed PCI address (%s).",
					interfaceField.Child("name").String(),
					iface.PciAddress,
				),
				Field: interfaceField.Child("pciAddress").String(),
			}}
		}
	}
	return nil
}

func validatePortConfiguration(interfaceField *k8sfield.Path, iface v1.Interface, network v1.Network) []metav1.StatusCause {
	var causes []metav1.StatusCause
	if network.Pod != nil {
		if iface.Ports != nil && iface.ExcludedPorts != nil {
			causes = append(causes, metav1.StatusCause{
				Type: metav1.CauseTypeFieldValueInvalid,
				Message: fmt.Sprintf(
					"Cannot define both ports to be forwarded and excluded ones on interface %s",
					interfaceField.Child("name").String(),
				),
				Field: interfaceField.Child("name").String(),
			})
		}
		if iface.Ports != nil {
			portsField := interfaceField.Child("ports")
			causes = append(causes, validatePorts(portsField, iface.Ports)...)
		}
		if iface.ExcludedPorts != nil {
			if iface.Masquerade == nil {
				causes = append(causes, metav1.StatusCause{
					Type: metav1.CauseTypeFieldValueInvalid,
					Message: fmt.Sprintf(
						"Excluded ports from forwarding allowed only on masquerade interfaces (%s)",
						interfaceField.Child("name").String(),
					),
					Field: interfaceField.Child("name").String(),
				})
			}
			portsField := interfaceField.Child("excludedPorts")
			causes = append(causes, validatePorts(portsField, iface.ExcludedPorts)...)
		}
	}
	return causes
}

func validatePorts(portsField *k8sfield.Path, ports []v1.Port) (causes []metav1.StatusCause) {
	portMap := map[string]struct{}{}
	for portIdx, port := range ports {
		var portField = portsField.Index(portIdx)

		if port.Name != "" {
			if _, ok := portMap[port.Name]; ok {
				causes = append(causes, metav1.StatusCause{
					Type:    metav1.CauseTypeFieldValueDuplicate,
					Message: fmt.Sprintf("Duplicate name of the port: %s", port.Name),
					Field:   portField.Child("name").String(),
				})
			}
			causes = append(causes, validatePortName(portField, port)...)
			portMap[port.Name] = struct{}{}
		}
		causes = append(causes, validatePortNonZero(portField, port)...)
		causes = append(causes, validatePortInRange(portField, port)...)
		causes = append(causes, validatePortProtocol(portField, port)...)
	}
	return
}

func validatePortName(portField *k8sfield.Path, port v1.Port) (causes []metav1.StatusCause) {
	if msgs := k8svalidation.IsValidPortName(port.Name); len(msgs) != 0 {
		causes = append(causes, metav1.StatusCause{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Message: fmt.Sprintf("Invalid name of the port: %s", port.Name),
			Field:   portField.Child("name").String(),
		})
	}
	return causes
}

func validatePortProtocol(portField *k8sfield.Path, port v1.Port) (causes []metav1.StatusCause) {
	if port.Protocol != "" {
		if port.Protocol != "TCP" && port.Protocol != "UDP" {
			causes = append(causes, metav1.StatusCause{
				Type:    metav1.CauseTypeFieldValueInvalid,
				Message: "Unknown protocol, only TCP or UDP allowed",
				Field:   portField.Child("protocol").String(),
			})
		}
	}
	return causes
}

func validatePortInRange(portField *k8sfield.Path, port v1.Port) (causes []metav1.StatusCause) {
	if port.Port < 0 || port.Port > 65536 {
		causes = append(causes, metav1.StatusCause{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Message: "Port field must be in range 0 < x < 65536.",
			Field:   portField.String(),
		})
	}
	return causes
}

func validatePortNonZero(portField *k8sfield.Path, port v1.Port) (causes []metav1.StatusCause) {
	if port.Port == 0 {
		causes = append(causes, metav1.StatusCause{
			Type:    metav1.CauseTypeFieldValueRequired,
			Message: "Port field is mandatory.",
			Field:   portField.String(),
		})
	}
	return causes
}

func validateDHCPOptions(interfaceField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	dhcpField := interfaceField.Child("dhcpOptions")
	var causes []metav1.StatusCause
	if iface.DHCPOptions != nil {
		causes = append(causes, validateDHCPExtraOptions(dhcpField, iface)...)
		causes = append(causes, validateDHCPNTPServersAreValidIPv4Addresses(dhcpField, iface)...)
	}
	return causes
}

func validateDHCPExtraOptions(dhcpField *k8sfield.Path, iface v1.Interface) []metav1.StatusCause {
	var causes []metav1.StatusCause
	privateOptions := iface.DHCPOptions.PrivateOptions
	if countUniqueDHCPPrivateOptions(privateOptions) < len(privateOptions) {
		causes = append(causes, metav1.StatusCause{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Message: "Found Duplicates: you have provided duplicate DHCPPrivateOptions",
			Field:   dhcpField.String(),
		})
	}

	for idx, DHCPPrivateOption := range privateOptions {
		privateOptionField := dhcpField.Child("privateOptions").Index(idx)
		causes = append(causes, validateDHCPPrivateOptionsWithinRange(privateOptionField, DHCPPrivateOption)...)
	}
	return causes
}

func validateDHCPNTPServersAreValidIPv4Addresses(dhcpField *k8sfield.Path, iface v1.Interface) (causes []metav1.StatusCause) {
	if iface.DHCPOptions != nil {
		for idx, ip := range iface.DHCPOptions.NTPServers {
			if net.ParseIP(ip).To4() == nil {
				causes = append(causes, metav1.StatusCause{
					Type:    metav1.CauseTypeFieldValueInvalid,
					Message: "NTP servers must be a list of valid IPv4 addresses.",
					Field:   dhcpField.Child("ntpServers").Index(idx).String(),
				})
			}
		}
	}
	return causes
}

func validateDHCPPrivateOptionsWithinRange(optionField *k8sfield.Path, dhcpPrivateOption v1.DHCPPrivateOptions) (causes []metav1.StatusCause) {
	if !(dhcpPrivateOption.Option >= 224 && dhcpPrivateOption.Option <= 254) {
		causes = append(causes, metav1.StatusCause{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Message: "provided DHCPPrivateOptions are out of range, must be in range 224 to 254",
			Field:   optionField.String(),
		})
	}
	return causes
}

func countUniqueDHCPPrivateOptions(privateOptions []v1.DHCPPrivateOptions) int {
	optionSet := map[int]struct{}{}
	for _, DHCPPrivateOption := range privateOptions {
		optionSet[DHCPPrivateOption.Option] = struct{}{}
	}
	return len(optionSet)
}
