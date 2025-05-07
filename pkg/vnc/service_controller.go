package vnc

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/pointer"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/log"
)

// ServiceController handles creating and deleting services for VMIs with DirectVNCAccess
type ServiceController struct {
	clientset               kubernetes.Interface
	vmiInformer             cache.SharedIndexInformer
	serviceInformer         cache.SharedIndexInformer
	queue                   workqueue.RateLimitingInterface
	vmiStore                cache.Store
	serviceStore            cache.Store
	lastSyncResourceVersion string
}

// NewVNCServiceController creates a new controller for managing VNC services
func NewVNCServiceController(clientset kubernetes.Interface, vmiInformer cache.SharedIndexInformer, serviceInformer cache.SharedIndexInformer) *ServiceController {
	ctrl := &ServiceController{
		clientset:       clientset,
		vmiInformer:     vmiInformer,
		serviceInformer: serviceInformer,
		queue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vmi-vnc-services"),
		vmiStore:        vmiInformer.GetStore(),
		serviceStore:    serviceInformer.GetStore(),
	}

	vmiInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addVMI,
		UpdateFunc: ctrl.updateVMI,
		DeleteFunc: ctrl.deleteVMI,
	})

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addService,
		UpdateFunc: ctrl.updateService,
		DeleteFunc: ctrl.deleteService,
	})

	return ctrl
}

// LastSyncResourceVersion returns the last synced resource version
func (c *ServiceController) LastSyncResourceVersion() string {
	return c.lastSyncResourceVersion
}

// HasSynced returns true if the controller has synced its caches
func (c *ServiceController) HasSynced() bool {
	return c.vmiInformer.HasSynced() && c.serviceInformer.HasSynced()
}

func (c *ServiceController) addVMI(obj interface{}) {
	vmi := obj.(*v1.VirtualMachineInstance)
	log.Log.Infof("Adding VMI %s/%s", vmi.Namespace, vmi.Name)

	if vmi.Spec.DirectVNCAccess != nil {
		c.enqueueVMI(vmi)
	}
}

func (c *ServiceController) updateVMI(old, cur interface{}) {
	curVMI := cur.(*v1.VirtualMachineInstance)
	oldVMI := old.(*v1.VirtualMachineInstance)

	if curVMI.ResourceVersion == oldVMI.ResourceVersion {
		return
	}

	if curVMI.Spec.DirectVNCAccess != nil || oldVMI.Spec.DirectVNCAccess != nil {
		c.enqueueVMI(curVMI)
	}
}

func (c *ServiceController) deleteVMI(obj interface{}) {
	vmi, ok := obj.(*v1.VirtualMachineInstance)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			log.Log.Errorf("Couldn't get object from tombstone %+v", obj)
			return
		}
		vmi, ok = tombstone.Obj.(*v1.VirtualMachineInstance)
		if !ok {
			log.Log.Errorf("Tombstone contained object that is not a VMI %+v", obj)
			return
		}
	}

	log.Log.Infof("Deleting VMI %s/%s", vmi.Namespace, vmi.Name)
	c.enqueueVMI(vmi)
}

func (c *ServiceController) addService(obj interface{}) {
	// We don't need to do anything specific when services are added
}

func (c *ServiceController) updateService(old, cur interface{}) {
	// We don't need to do anything specific when services are updated
}

func (c *ServiceController) deleteService(obj interface{}) {
	// We don't need to do anything specific when services are deleted
}

func (c *ServiceController) enqueueVMI(vmi *v1.VirtualMachineInstance) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vmi)
	if err != nil {
		log.Log.Errorf("Failed to extract key from VMI: %v", err)
		return
	}
	c.queue.Add(key)
}

// Run starts the controller
func (svc *ServiceController) Run(stopCh <-chan struct{}) {
	defer svc.queue.ShutDown()

	log.Log.Info("Starting VNC service controller")
	defer log.Log.Info("Shutting down VNC service controller")

	// Wait for the caches to be synced before starting workers
	if !cache.WaitForCacheSync(stopCh, svc.vmiInformer.HasSynced, svc.serviceInformer.HasSynced) {
		log.Log.Error("Timed out waiting for caches to sync")
		return
	}
	log.Log.Info("VNC service controller caches have synced")

	// Use 3 worker threads (KubeVirt default for most controllers)
	const workers = 3

	// Start the workers
	for i := 0; i < workers; i++ {
		go wait.Until(svc.runWorker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *ServiceController) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *ServiceController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.sync(key.(string))
	if err == nil {
		c.queue.Forget(key)
		return true
	}

	log.Log.Errorf("Failed to sync %v: %v", key, err)
	c.queue.AddRateLimited(key)

	return true
}

func (c *ServiceController) sync(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	vmi, exists, err := c.vmiStore.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		// VMI was deleted, clean up any VNC service
		return c.deleteVNCService(namespace, name)
	}

	vmiObj := vmi.(*v1.VirtualMachineInstance)
	if vmiObj.Spec.DirectVNCAccess == nil {
		// DirectVNCAccess was removed, delete the service if it exists
		return c.deleteVNCService(namespace, name)
	}

	// VMI with DirectVNCAccess exists, ensure service exists
	return c.createOrUpdateVNCService(vmiObj)
}

func (c *ServiceController) createOrUpdateVNCService(vmi *v1.VirtualMachineInstance) error {
	serviceName := fmt.Sprintf("%s-vnc", vmi.Name)

	// Get the VNC port from DirectVNCAccess, default to 5900
	port := int32(5900)
	if vmi.Spec.DirectVNCAccess.Port > 0 {
		port = vmi.Spec.DirectVNCAccess.Port
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: vmi.Namespace,
			Labels: map[string]string{
				"kubevirt.io/created-by":      string(vmi.UID),
				"app.kubernetes.io/component": "vnc-access",
				"kubevirt.io/vmi":             vmi.Name,
			},
			Annotations: map[string]string{
				"kubevirt.io/vnc-port": fmt.Sprintf("%d", port),
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "vnc",
					Protocol:   corev1.ProtocolTCP,
					Port:       port,
					TargetPort: intstr.FromInt(int(port)),
				},
			},
			Selector: map[string]string{
				"kubevirt.io/created-by": string(vmi.UID),
			},
			Type:                  corev1.ServiceTypeNodePort,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
		},
	}

	// Set owner reference to the VMI
	ownerRef := metav1.OwnerReference{
		APIVersion: v1.GroupVersion.String(),
		Kind:       "VirtualMachineInstance",
		Name:       vmi.Name,
		UID:        vmi.UID,
		Controller: pointer.BoolPtr(true),
	}
	service.OwnerReferences = []metav1.OwnerReference{ownerRef}

	// Check if service already exists
	existingService, err := c.clientset.CoreV1().Services(vmi.Namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil {
		// Service exists, check if it needs updates
		existingService.Spec = service.Spec
		existingService.Labels = service.Labels
		existingService.Annotations = service.Annotations

		_, err = c.clientset.CoreV1().Services(vmi.Namespace).Update(context.TODO(), existingService, metav1.UpdateOptions{})
		return err
	}

	if k8serrors.IsNotFound(err) {
		// Create new service
		_, err = c.clientset.CoreV1().Services(vmi.Namespace).Create(context.TODO(), service, metav1.CreateOptions{})
		if err == nil {
			log.Log.Infof("Created VNC service %s/%s for VMI %s", vmi.Namespace, serviceName, vmi.Name)
		}
		return err
	}

	return err
}

func (c *ServiceController) deleteVNCService(namespace, vmiName string) error {
	serviceName := fmt.Sprintf("%s-vnc", vmiName)

	err := c.clientset.CoreV1().Services(namespace).Delete(context.TODO(), serviceName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	if err == nil {
		log.Log.Infof("Deleted VNC service %s/%s", namespace, serviceName)
	}

	return nil
}
