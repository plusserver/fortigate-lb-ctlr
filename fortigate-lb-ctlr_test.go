package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Nexinto/go-fortigate-client/fortigate"

	"github.com/Nexinto/k8s-lbutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	ipamfake "github.com/Nexinto/k8s-ipam/pkg/client/clientset/versioned/fake"
)

// Create a test environment with some useful defaults.
func testEnvironment() *Controller {

	c := New(fake.NewSimpleClientset(), ipamfake.NewSimpleClientset(), fortigate.NewFakeClient())

	c.Tag = "testing"

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
	a.Equal("testing", vip80.Comment)
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
	a.Equal("testing", vip443.Comment)
	a.Equal(3, len(vip443.Realservers))
	a.Equal("10.100.11.1", vip443.Realservers[0].Ip)
	a.Equal(36123, vip443.Realservers[0].Port)
	a.Equal("10.100.11.2", vip443.Realservers[1].Ip)
	a.Equal(36123, vip443.Realservers[1].Port)
	a.Equal("10.100.11.3", vip443.Realservers[2].Ip)
	a.Equal(36123, vip443.Realservers[2].Port)
}

// Test a Service with 2 Ports with ManagePolicy Mode
func TestDefaultVipAndPolicies(t *testing.T) {
	a := assert.New(t)
	c := testEnvironment()
	c.ManagePolicy = true

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
	a.Equal("testing", vip80.Comment)
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
	a.Equal("testing", vip443.Comment)
	a.Equal(3, len(vip443.Realservers))
	a.Equal("10.100.11.1", vip443.Realservers[0].Ip)
	a.Equal(36123, vip443.Realservers[0].Port)
	a.Equal("10.100.11.2", vip443.Realservers[1].Ip)
	a.Equal(36123, vip443.Realservers[1].Port)
	a.Equal("10.100.11.3", vip443.Realservers[2].Ip)
	a.Equal(36123, vip443.Realservers[2].Port)

	pol80, err := c.Fortigate.GetFirewallPolicyByName(assigned + ":80")
	if !a.Nil(err) {
		return
	}

	a.Equal(1, len(pol80.Srcintf))
	a.Equal("any", pol80.Srcintf[0].Name)
	a.Equal(1, len(pol80.Dstintf))
	a.Equal("any", pol80.Dstintf[0].Name)
	a.Equal(1, len(pol80.Srcaddr))
	a.Equal("all", pol80.Srcaddr[0].Name)
	a.Equal(1, len(pol80.Dstaddr))
	a.Equal("default-myservice-80", pol80.Dstaddr[0].Name)
	a.Equal(1, len(pol80.Service))
	a.Equal("ALL", pol80.Service[0].Name)

	pol443, err := c.Fortigate.GetFirewallPolicyByName(assigned + ":443")
	if !a.Nil(err) {
		return
	}

	a.Equal(1, len(pol443.Srcintf))
	a.Equal("any", pol443.Srcintf[0].Name)
	a.Equal(1, len(pol443.Dstintf))
	a.Equal("any", pol443.Dstintf[0].Name)
	a.Equal(1, len(pol443.Srcaddr))
	a.Equal("all", pol443.Srcaddr[0].Name)
	a.Equal(1, len(pol443.Dstaddr))
	a.Equal("default-myservice-443", pol443.Dstaddr[0].Name)
	a.Equal(1, len(pol443.Service))
	a.Equal("ALL", pol443.Service[0].Name)
}

// Test cleaning up unused resources
func TestHousekeeping(t *testing.T) {
	a := assert.New(t)
	c := testEnvironment()
	c.ManagePolicy = true

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

	// The following resources are owned by us, but weren't cleaned up
	// properly so they will have to be picked up by housekeeping.
	if _, err := c.Fortigate.CreateVIP(&fortigate.VIP{
		Name:            "default-oldservice-80",
		Type:            fortigate.VIPTypeServerLoadBalance,
		LdbMethod:       fortigate.VIPLdbMethodRoundRobin,
		PortmappingType: fortigate.VIPPortmappingType1To1,
		Extintf:         "any",
		ServerType:      fortigate.VIPServerTypeTcp,
		Comment:         "testing",
		Extip:           "10.10.10.10",
		Extport:         "80",
		Realservers: []fortigate.VIPRealservers{
			{Ip: "10.100.11.1", Port: 35314},
			{Ip: "10.100.11.2", Port: 35314},
			{Ip: "10.100.11.3", Port: 35314},
		}}); !a.Nil(err) {
		return
	}

	if _, err := c.Fortigate.CreateFirewallPolicy(&fortigate.FirewallPolicy{
		Name:     "10.10.10.10:80",
		Srcintf:  []fortigate.FirewallPolicySrcintf{{Name: "any"}},
		Dstintf:  []fortigate.FirewallPolicyDstintf{{Name: "any"}},
		Srcaddr:  []fortigate.FirewallPolicySrcaddr{{Name: "all"}},
		Dstaddr:  []fortigate.FirewallPolicyDstaddr{{Name: "default-oldservice-80"}},
		Service:  []fortigate.FirewallPolicyService{{Name: "ALL"}},
		Action:   fortigate.FirewallPolicyActionAccept,
		Comments: "testing",
	}); !a.Nil(err) {
		return
	}

	// The following resources were not created by us, so housekeeping may not touch them.
	if _, err := c.Fortigate.CreateVIP(&fortigate.VIP{
		Name:            "mywebvip",
		Type:            fortigate.VIPTypeServerLoadBalance,
		LdbMethod:       fortigate.VIPLdbMethodRoundRobin,
		PortmappingType: fortigate.VIPPortmappingType1To1,
		Extintf:         "any",
		ServerType:      fortigate.VIPServerTypeTcp,
		Comment:         "i did this",
		Extip:           "10.10.10.110",
		Extport:         "80",
		Realservers: []fortigate.VIPRealservers{
			{Ip: "10.100.30.1", Port: 80},
			{Ip: "10.100.30.2", Port: 80},
			{Ip: "10.100.30.3", Port: 80},
		}}); !a.Nil(err) {
		return
	}

	if _, err := c.Fortigate.CreateFirewallPolicy(&fortigate.FirewallPolicy{
		Name:     "10.10.10.110:80",
		Srcintf:  []fortigate.FirewallPolicySrcintf{{Name: "any"}},
		Dstintf:  []fortigate.FirewallPolicyDstintf{{Name: "any"}},
		Srcaddr:  []fortigate.FirewallPolicySrcaddr{{Name: "all"}},
		Dstaddr:  []fortigate.FirewallPolicyDstaddr{{Name: "mywebvip"}},
		Service:  []fortigate.FirewallPolicyService{{Name: "ALL"}},
		Action:   fortigate.FirewallPolicyActionAccept,
		Comments: "this is great",
	}); !a.Nil(err) {
		return
	}

	if !a.Nil(c.simulate()) {
		return
	}

	c.Housekeeping(false)

	_, err := c.Fortigate.GetVIP("default-myservice-80")
	a.Nil(err)

	_, err = c.Fortigate.GetVIP("default-myservice-443")
	a.Nil(err)

	_, err = c.Fortigate.GetVIP("default-oldservice-80")
	a.Error(err, "default-oldservice-80 should have been deleted")

	_, err = c.Fortigate.GetFirewallPolicyByName("10.10.10.10:80")
	a.Error(err, "10.10.10.10:80 should have been deleted")

	_, err = c.Fortigate.GetVIP("mywebvip")
	a.Nil(err, "mywebvip should not have been deleted")

	_, err = c.Fortigate.GetFirewallPolicyByName("10.10.10.110:80")
	a.Nil(err, "10.10.10.110:80 should not have been deleted")

}
