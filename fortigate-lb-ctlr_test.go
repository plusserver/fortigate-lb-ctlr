package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Nexinto/go-fortigate-client/fortigate"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	ipamfake "github.com/Nexinto/k8s-ipam/pkg/client/clientset/versioned/fake"
	"github.com/Nexinto/k8s-lbutil"
	"k8s.io/client-go/tools/cache"
	"time"
)

// Create a test environment with some useful defaults.
func testEnvironment() *Controller {

	c := New(fake.NewSimpleClientset(), ipamfake.NewSimpleClientset(), fortigate.NewFakeClient())

	c.Tag = "faketesting"

	c.Kubernetes.CoreV1().Namespaces().Create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})

	c.Kubernetes.CoreV1().Nodes().Create(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Address: "10.100.11.1", Type: corev1.NodeInternalIP}}},
	})
	c.Kubernetes.CoreV1().Nodes().Create(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node2"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Address: "10.100.11.2", Type: corev1.NodeInternalIP}}},
	})
	c.Kubernetes.CoreV1().Nodes().Create(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node3"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Address: "10.100.11.3", Type: corev1.NodeInternalIP}}},
	})

	c.Initialize()
	go c.Start()

	stopCh := make(chan struct{})

	go c.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.ServiceSynced, c.IpAddressSynced) {
		panic("Timed out waiting for caches to sync")
	}

	return c
}

// simulate the behaviour of the controllers we depend on
// and give the parallel processes time to do their thing
func (c *Controller) simulate() error {

	time.Sleep(1 * time.Second)

	if err := lbutil.SimIPAM(c.IpamClient); err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	return nil
}

// Test a Service with 2 Ports.
func TestDefaultVip(t *testing.T) {
	a := assert.New(t)
	c := testEnvironment()

	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myservice",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort,
			Ports: []corev1.ServicePort{
				{
					Port:     80,
					NodePort: 33978,
				},
				{
					Port:     443,
					NodePort: 36123,
				},
			},
		},
	}

	c.Kubernetes.CoreV1().Services("default").Create(s)

	if !a.Nil(c.simulate()) {
		return
	}

	addr, err := c.IpamClient.IpamV1().IpAddresses("default").Get("myservice", metav1.GetOptions{})
	if !a.Nil(err, "an address object was created") {
		return
	}

	a.NotNil(addr)

	assigned := addr.Status.Address

	if !a.NotEmpty(assigned, "an address was assigned") {
		return
	}

	s, err = c.Kubernetes.CoreV1().Services("default").Get("myservice", metav1.GetOptions{})
	if !a.Nil(err) {
		return
	}
	a.Equal(assigned, s.Annotations[lbutil.AnnNxVIP], "we want our VIP")

	vip80, err := c.Fortigate.GetVIP("default-myservice-80")
	if !a.Nil(err) {
		return
	}
	a.Equal(assigned, vip80.Extip)
	a.Equal(3, len(vip80.Realservers))
	a.Equal("10.100.11.1", vip80.Realservers[0].Ip)
	a.Equal(33978, vip80.Realservers[0].Port)
	a.Equal("10.100.11.2", vip80.Realservers[1].Ip)
	a.Equal(33978, vip80.Realservers[1].Port)
	a.Equal("10.100.11.3", vip80.Realservers[2].Ip)
	a.Equal(33978, vip80.Realservers[2].Port)

	vip443, err := c.Fortigate.GetVIP("default-myservice-443")
	if !a.Nil(err) {
		return
	}
	a.Equal(assigned, vip443.Extip)
	a.Equal(3, len(vip443.Realservers))
	a.Equal("10.100.11.1", vip443.Realservers[0].Ip)
	a.Equal(36123, vip443.Realservers[0].Port)
	a.Equal("10.100.11.2", vip443.Realservers[1].Ip)
	a.Equal(36123, vip443.Realservers[1].Port)
	a.Equal("10.100.11.3", vip443.Realservers[2].Ip)
	a.Equal(36123, vip443.Realservers[2].Port)
}
