// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"fmt"

	"github.com/tsuru/kubernetes-router/router"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/pkg/api/v1"
)

// managedServiceLabel is added to every service created by the router
const managedServiceLabel = "tsuru.io/router-lb"

// defaultLBPort is the default exposed ports to the LB
var defaultLBPorts = []int{80, 443}

// LBService manages LoadBalancer services
type LBService struct {
	*BaseService
	Ports []int
}

// Create creates a LoadBalancer type service without any selectors
func (s *LBService) Create(appName string, labels map[string]string) error {
	if len(s.Ports) == 0 {
		s.Ports = defaultLBPorts
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName(appName),
			Namespace:   s.Namespace,
			Labels:      map[string]string{appLabel: appName, managedServiceLabel: "true"},
			Annotations: s.Annotations,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}
	for i := range s.Ports {
		service.Spec.Ports = append(service.Spec.Ports, v1.ServicePort{
			Name:       fmt.Sprintf("port-%d", s.Ports[i]),
			Protocol:   v1.ProtocolTCP,
			Port:       int32(s.Ports[i]),
			TargetPort: intstr.FromInt(defaultServicePort),
		})
	}
	for k, v := range s.Labels {
		service.ObjectMeta.Labels[k] = v
	}
	for k, v := range labels {
		service.ObjectMeta.Labels[k] = v
	}
	_, err = client.CoreV1().Services(s.Namespace).Create(service)
	if k8sErrors.IsAlreadyExists(err) {
		return router.ErrIngressAlreadyExists
	}
	return err
}

// Remove removes the LoadBalancer service
func (s *LBService) Remove(appName string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}
	service, err := s.getLBService(appName)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if dstApp, swapped := s.BaseService.isSwapped(service.ObjectMeta); swapped {
		return ErrAppSwapped{App: appName, DstApp: dstApp}
	}
	err = client.CoreV1().Services(s.Namespace).Delete(service.Name, &metav1.DeleteOptions{})
	if k8sErrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Update updates the LoadBalancer service copying the web service
// labels, selectors, annotations and ports
func (s *LBService) Update(appName string) error {
	if len(s.Ports) == 0 {
		s.Ports = defaultLBPorts
	}
	lbService, err := s.getLBService(appName)
	if err != nil {
		return err
	}
	if swappedApp, ok := lbService.Labels[swapLabel]; ok {
		appName = swappedApp
	}
	webService, err := s.getWebService(appName)
	if err != nil {
		return err
	}
	for k, v := range webService.Labels {
		if _, ok := s.Labels[k]; ok {
			continue
		}
		lbService.Labels[k] = v
	}
	for k, v := range webService.Annotations {
		if _, ok := s.Annotations[k]; ok {
			continue
		}
		lbService.Annotations[k] = v
	}
	lbService.Spec.Selector = webService.Spec.Selector
	client, err := s.getClient()
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Services(s.Namespace).Update(lbService)
	return err
}

// Swap swaps the two LB services selectors
func (s *LBService) Swap(appSrc string, appDst string) error {
	srcServ, err := s.getLBService(appSrc)
	if err != nil {
		return err
	}
	dstServ, err := s.getLBService(appDst)
	if err != nil {
		return err
	}
	s.swap(srcServ, dstServ)
	client, err := s.getClient()
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Services(s.Namespace).Update(srcServ)
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Services(s.Namespace).Update(dstServ)
	if err != nil {
		s.swap(srcServ, dstServ)
		_, errRollback := client.CoreV1().Services(s.Namespace).Update(srcServ)
		if errRollback != nil {
			return fmt.Errorf("failed to rollback swap %v: %v", err, errRollback)
		}
	}
	return err
}

// Get returns the LoadBalancer IP
func (s *LBService) Get(appName string) (map[string]string, error) {
	service, err := s.getLBService(appName)
	if err != nil {
		return nil, err
	}
	var addr string
	lbs := service.Status.LoadBalancer.Ingress
	if len(lbs) != 0 {
		addr = lbs[0].IP
		ports := service.Spec.Ports
		if len(ports) != 0 {
			addr = fmt.Sprintf("%s:%d", addr, ports[0].Port)
		}
	}
	return map[string]string{"address": addr}, nil
}

func (s *LBService) getLBService(appName string) (*v1.Service, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.CoreV1().Services(s.Namespace).Get(serviceName(appName), metav1.GetOptions{})
}

func (s *LBService) swap(srcServ, dstServ *v1.Service) {
	srcServ.Spec.Selector, dstServ.Spec.Selector = dstServ.Spec.Selector, srcServ.Spec.Selector
	s.BaseService.swap(&srcServ.ObjectMeta, &dstServ.ObjectMeta)
}

func serviceName(app string) string {
	return fmt.Sprintf("%s-router-lb", app)
}
