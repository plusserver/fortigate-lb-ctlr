package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/Nexinto/k8s-lbutil"

	log "github.com/sirupsen/logrus"

	"github.com/Nexinto/go-fortigate-client/fortigate"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ipamv1 "github.com/Nexinto/k8s-ipam/pkg/apis/ipam.nexinto.com/v1"
	ipamclientset "github.com/Nexinto/k8s-ipam/pkg/client/clientset/versioned"
)

const (
	// fortigate provider
	AnnNxVIPProviderFortigate = "fortigate"
)

func main() {

	flag.Parse()

	// If this is not set, glog tries to log into something below /tmp which doesn't exist.
	flag.Lookup("log_dir").Value.Set("/")

	if e := os.Getenv("LOG_LEVEL"); e != "" {
		if l, err := log.ParseLevel(e); err == nil {
			log.SetLevel(l)
		} else {
			log.SetLevel(log.WarnLevel)
			log.Warnf("unknown log level %s, setting to 'warn'", e)
		}
	}

	var tag string

	fg, err := fortigate.NewWebClient(fortigate.WebClient{
		URL:      os.Getenv("FORTIGATE_URL"),
		ApiKey:   os.Getenv("FORTIGATE_API_KEY"),
		User:     os.Getenv("FORTIGATE_USER"),
		Password: os.Getenv("FORTIGATE_PASSWORD"),
		Log:      os.Getenv("FORTIGATE_DEBUG") == "true"})
	if err != nil {
		panic(err.Error())
	}

	if e := os.Getenv("CONTROLLER_TAG"); e != "" {
		tag = e
	} else {
		tag = "fortigate-lb-ctlr"
	}

	var kubeconfig string

	if e := os.Getenv("KUBECONFIG"); e != "" {
		kubeconfig = e
	}

	clientConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	ipamclient, err := ipamclientset.NewForConfig(clientConfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		panic(err.Error())
	}

	c := New(clientset, ipamclient, fg)

	if e := os.Getenv("FORTIGATE_MANAGE_POLICY"); e != "" && e != "false" {
		c.ManagePolicy = true
	} else {
		c.ManagePolicy = false
	}

	if e := os.Getenv("FORTIGATE_REALSERVER_LIMIT"); e != "" {
		fmt.Sscanf(e, "%d", &c.RealserverLimit)
	} else {
		c.RealserverLimit = -1
	}

	if e := os.Getenv("REQUIRE_TAG"); e != "" && e != "false" {
		c.RequireTag = true
	} else {
		c.RequireTag = false
	}

	c.Tag = tag
	c.Fortigate = fg
	c.ActiveNodes = map[string]string{}
	c.ActiveNodesMutex = sync.Mutex{}

	c.Initialize()
	c.Start()
}

func New(clientset kubernetes.Interface, ipamclient ipamclientset.Interface, fg fortigate.Client) *Controller {

	c := &Controller{
		Kubernetes: clientset,
		IpamClient: ipamclient,
		Fortigate:  fg,

		ActiveNodes:      map[string]string{},
		ActiveNodesMutex: sync.Mutex{},
	}

	return c
}

func (c *Controller) IpAddressCreatedOrUpdated(address *ipamv1.IpAddress) error {
	log.Debugf("processing address '%s-%s'", address.Namespace, address.Name)
	lbutil.IpAddressCreatedOrUpdated(c.ServiceQueue, address)
	return nil
}

func (c *Controller) IpAddressDeleted(address *ipamv1.IpAddress) error {
	log.Debugf("processing deleted address '%s-%s'", address.Namespace, address.Name)
	return lbutil.IpAddressDeleted(c.Kubernetes, c.ServiceLister, address)
}

func (c *Controller) ServiceCreatedOrUpdated(service *corev1.Service) error {
	log.Debugf("processing service '%s-%s'", service.Namespace, service.Name)

	ok, needsUpdate, newservice, err := lbutil.EnsureVIP(c.Kubernetes, c.IpamClient, c.IpAddressLister, service, AnnNxVIPProviderFortigate, c.RequireTag)
	if err != nil {
		return fmt.Errorf("error getting vip for service '%s-%s': %s", service.Namespace, service.Name, err.Error())
	} else if !ok {
		if needsUpdate {
			_, err = c.Kubernetes.CoreV1().Services(service.Namespace).Update(newservice)
			return err
		}
		return nil
	}

	if needsUpdate {
		_, err = c.Kubernetes.CoreV1().Services(service.Namespace).Update(newservice)
		return err
	}

	service = newservice

	if err := c.syncService(service); err != nil {
		return fmt.Errorf("error syncing vips for service '%s-%s': %s", service.Namespace, service.Name, err.Error())
	}

	if c.ManagePolicy {
		if err := c.syncServiceForPolicy(service); err != nil {
			return fmt.Errorf("error syncing policies for service '%s-%s': %s", service.Namespace, service.Name, err.Error())
		}
	}

	if service.Annotations[lbutil.AnnNxVIP] != service.Annotations[lbutil.AnnNxAssignedVIP] {
		service.Annotations[lbutil.AnnNxVIP] = service.Annotations[lbutil.AnnNxAssignedVIP]
		_, err = c.Kubernetes.CoreV1().Services(service.Namespace).Update(service)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) syncServiceForPolicy(service *corev1.Service) error {
	policies := c.policiesFor(service)
	for _, policy := range policies {
		if err := c.syncServicePortForPolicy(policy, service); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) syncServicePortForPolicy(policy fortigate.FirewallPolicy, service *corev1.Service) error {
	log.Debugf("syncing policy '%s'", policy.Name)
	if _, err := c.Fortigate.GetFirewallPolicyByName(policy.Name); err != nil {
		if _, err := c.Fortigate.CreateFirewallPolicy(&policy); err != nil {
			return fmt.Errorf("error creating policy for '%s': %s", policy.Name, err.Error())
		} else {
			log.Infof("created policy '%s'", policy.Name)
		}
	}
	return nil
}

func (c *Controller) syncService(service *corev1.Service) (err error) {
	if service.Annotations[lbutil.AnnNxAssignedVIP] == "" {
		return fmt.Errorf("called syncService(%s-%s), but VIP is not set", service.Namespace, service.Name)
	}

	vips := c.vipsFor(service)
	for _, vip := range vips {
		if err = c.syncServicePort(vip, service); err != nil {
			return
		}
	}
	return
}

func (c *Controller) syncServicePort(vip fortigate.VIP, service *corev1.Service) error {
	log.Debugf("syncing vip '%s'", vip.Name)
	currentVip, err := c.Fortigate.GetVIP(vip.MKey())
	if err == nil {
		if !c.vipUpToDate(&vip, currentVip) {
			if err := c.Fortigate.UpdateVIP(&vip); err != nil {
				return fmt.Errorf("error updating vip '%s': %s", vip.Name, err.Error())
			} else {
				log.Infof("updated vip for '%s'", vip.Name)
			}
		}
	} else {
		if _, err := c.Fortigate.CreateVIP(&vip); err != nil {
			return fmt.Errorf("error creating vip '%s': %s", vip.Name, err.Error())
		} else {
			log.Infof("created vip '%s'", vip.Name)
		}
	}
	return nil
}

func (c *Controller) ServiceDeleted(service *corev1.Service) error {
	name := fmt.Sprintf("%s-%s", service.Namespace, service.Name)
	log.Debugf("processing deleted service '%s'", name)

	if service.Annotations[lbutil.AnnNxVIPActiveProvider] != AnnNxVIPProviderFortigate {
		log.Debugf("skipping service '%s', it is managed by provider '%s'", name, service.Annotations[lbutil.AnnNxVIPActiveProvider])
		return nil
	}

	if c.ManagePolicy {
		for _, p := range c.policiesFor(service) {
			pp, err := c.Fortigate.GetFirewallPolicyByName(p.Name)
			if err != nil {
				return fmt.Errorf("error deleting policy '%s': %s", p.Name, err.Error())
			}
			if err := c.Fortigate.DeleteFirewallPolicy(pp.MKey()); err != nil {
				return fmt.Errorf("error deleting policy '%s': %s", p.Name, err.Error())
			} else {
				log.Infof("deleted policy %s", p.Name)
			}
		}
	}

	for _, v := range c.vipsFor(service) {
		if err := c.Fortigate.DeleteVIP(v.MKey()); err != nil {
			return fmt.Errorf("error deleting vip '%s': %s", v.Name, err.Error())
		} else {
			log.Infof("deleted vip %s", v.Name)
		}
	}

	addr, err := c.IpAddressLister.IpAddresses(service.Namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		} else {
			return fmt.Errorf("error getting address '%s': %s", name, err.Error())
		}
	}

	if addr.OwnerReferences != nil && len(addr.OwnerReferences) == 1 && addr.OwnerReferences[0].Name == service.Name && addr.OwnerReferences[0].Kind == "Service" {
		if err := c.IpamClient.IpamV1().IpAddresses(service.Namespace).Delete(addr.Name, &metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("error deleting address '%s-%s': %s", addr.Namespace, addr.Name, err.Error())
		} else {
			log.Infof("delete ipaddress '%s-%s'", addr.Namespace, addr.Name)
		}
	}

	return nil
}

func (c *Controller) NodeCreatedOrUpdated(node *corev1.Node) error {
	log.Debugf("processing node %s", node.Name)

	if node.Spec.Unschedulable {
		c.ActiveNodesMutex.Lock()
		delete(c.ActiveNodes, node.Name)
		c.ActiveNodesMutex.Unlock()
		return nil
	}

	var ip string

	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			ip = a.Address
		}
	}

	if ip != "" {
		c.ActiveNodesMutex.Lock()
		c.ActiveNodes[node.Name] = ip
		c.ActiveNodesMutex.Unlock()
	} else {
		log.Errorf("did not get internal ip address for %s", node.Name)
		c.ActiveNodesMutex.Lock()
		delete(c.ActiveNodes, node.Name)
		c.ActiveNodesMutex.Unlock()
	}

	// TODO: reschedule services
	return nil
}

func (c *Controller) NodeDeleted(node *corev1.Node) error {
	log.Debugf("processing deleted node %s", node.Name)

	c.ActiveNodesMutex.Lock()
	delete(c.ActiveNodes, node.Name)
	c.ActiveNodesMutex.Unlock()

	// TODO: reschedule services
	return nil
}

// Get the IP addresses of all active nodes.
func (c *Controller) activeNodeAddresses() []string {
	var ips []string

	c.ActiveNodesMutex.Lock()
	for _, ip := range c.ActiveNodes {
		ips = append(ips, ip)
	}
	c.ActiveNodesMutex.Unlock()

	sort.Strings(ips)

	if len(ips) > 0 && c.RealserverLimit > 0 && len(ips) > c.RealserverLimit {
		return ips[0:c.RealserverLimit]
	} else {
		return ips
	}

}

func (c *Controller) fgName(service *corev1.Service, port *corev1.ServicePort) string {
	return fmt.Sprintf("%s-%s-%d", service.Namespace, service.Name, port.Port)
}

func (c *Controller) realserversFor(port *corev1.ServicePort) (realservers []fortigate.VIPRealservers) {
	for _, nip := range c.activeNodeAddresses() {
		realservers = append(realservers, fortigate.VIPRealservers{Ip: nip, Port: int(port.NodePort)})
	}
	return
}

func (c *Controller) vipFor(service *corev1.Service, port *corev1.ServicePort) fortigate.VIP {
	return fortigate.VIP{
		Name:            c.fgName(service, port),
		Type:            fortigate.VIPTypeServerLoadBalance,
		LdbMethod:       fortigate.VIPLdbMethodRoundRobin,
		PortmappingType: fortigate.VIPPortmappingType1To1,
		Extintf:         "any",
		ServerType:      fortigate.VIPServerTypeTcp,
		Comment:         c.Tag,
		Extip:           service.Annotations[lbutil.AnnNxAssignedVIP],
		Extport:         fmt.Sprintf("%d", port.Port),
		Realservers:     c.realserversFor(port),
	}
}

func (c *Controller) vipsFor(service *corev1.Service) (vips []fortigate.VIP) {
	for _, port := range service.Spec.Ports {
		vips = append(vips, c.vipFor(service, &port))
	}
	return
}

func (c *Controller) policyFor(service *corev1.Service, port *corev1.ServicePort) fortigate.FirewallPolicy {
	return fortigate.FirewallPolicy{
		Name:     c.fgName(service, port),
		Srcintf:  []fortigate.FirewallPolicySrcintf{{Name: "any"}},
		Dstintf:  []fortigate.FirewallPolicyDstintf{{Name: "any"}},
		Srcaddr:  []fortigate.FirewallPolicySrcaddr{{Name: "all"}},
		Dstaddr:  []fortigate.FirewallPolicyDstaddr{{Name: c.fgName(service, port)}},
		Service:  []fortigate.FirewallPolicyService{{Name: "ALL"}},
		Action:   fortigate.FirewallPolicyActionAccept,
		Comments: c.Tag,
	}
}

func (c *Controller) policiesFor(service *corev1.Service) (policies []fortigate.FirewallPolicy) {
	for _, port := range service.Spec.Ports {
		policies = append(policies, c.policyFor(service, &port))
	}
	return
}

func (c *Controller) vipUpToDate(wanted, current *fortigate.VIP) bool {
	if wanted.Extip != current.Extip {
		log.Debugf("vipUpToDate(%s): external IP changed from %s to %s", wanted.Name, current.Extip, wanted.Extip)
		return false
	}

	if wanted.Extport != current.Extport {
		log.Debugf("vipUpToDate(%s): external IP port from %s to %s", wanted.Name, current.Extport, wanted.Extport)
		return false
	}

	if len(wanted.Realservers) != len(current.Realservers) {
		log.Debugf("vipUpToDate(%s): different number of realservers (%d, %d)", wanted.Name, len(wanted.Realservers), len(current.Realservers))
		return false
	}

	for i := range wanted.Realservers {
		if wanted.Realservers[i].Ip != current.Realservers[i].Ip || wanted.Realservers[i].Port != current.Realservers[i].Port {
			log.Debugf("vipUpToDate(%s): different realservers (%s:%s, %s:%s)", wanted.Name, wanted.Realservers[i].Ip, wanted.Realservers[i].Port, current.Realservers[i].Ip, current.Realservers[i].Port)
			return false
		}
	}

	return true
}

func (c *Controller) syncVIPs() error {
	return nil
}
