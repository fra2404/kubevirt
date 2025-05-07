package watch

import (
	"k8s.io/client-go/tools/cache"

	"kubevirt.io/kubevirt/pkg/vnc"
)

// newVNCServiceController creates a new controller that manages VNC services for VMIs
func (vca *VirtControllerApp) newVNCServiceController() (cache.Controller, error) {
	// Get the existing clientset
	clientset := vca.clientSet

	// Get the VMI informer from the shared informers
	vmiInformer := vca.vmiInformer

	// Create a service informer with the right parameters
	serviceInformer := vca.informerFactory.K8SInformerFactory().Core().V1().Services().Informer()

	// Create the VNC controller
	vncController := vnc.NewVNCServiceController(
		clientset,
		vmiInformer,
		serviceInformer,
	)

	return vncController, nil
}
