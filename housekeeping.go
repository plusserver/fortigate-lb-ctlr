package main

import (
	"time"

	"github.com/Nexinto/k8s-lbutil"

	log "github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Controller) Housekeeping(loop bool) {
	for {
		services, err := c.Kubernetes.CoreV1().Services(metav1.NamespaceAll).List(metav1.ListOptions{})

		if err != nil {
			log.Errorf("error listing services: %s", err.Error())
			if loop {
				continue
			} else {
				return
			}
		}

		validVIPs := make(map[string]bool)
		validPolicies := make(map[string]bool)

		for _, service := range services.Items {
			if service.Spec.Type != corev1.ServiceTypeNodePort {
				continue
			}

			if c.RequireTag && len(service.Annotations[lbutil.AnnNxReqVIP]) <= 0 {
				continue
			}

			for _, v := range c.vipsFor(&service) {
				validVIPs[v.Name] = true
			}

			if c.ManagePolicy {
				for _, p := range c.policiesFor(&service) {
					validPolicies[p.Name] = true
				}
			}
		}

		if c.ManagePolicy {
			policies, err := c.Fortigate.ListFirewallPolicys()
			if err != nil {
				log.Errorf("error listing policies: %s", err.Error())
				if loop {
					continue
				} else {
					return
				}
			}

			for _, p := range policies {
				if p.Comments != c.Tag {
					continue
				}

				if !validPolicies[p.Name] {
					log.Infof("housekeeping: deleting obsolete policy '%s'", p.Name)
					err := c.Fortigate.DeleteFirewallPolicy(p.MKey())
					if err != nil {
						log.Errorf("error deleting policy '%s': %s", p.Name, err.Error())
					}
				}
			}
		}

		vips, err := c.Fortigate.ListVIPs()
		if err != nil {
			log.Errorf("error listing VIPs: %s", err.Error())
			if loop {
				continue
			} else {
				return
			}
		}

		for _, v := range vips {
			if v.Comment != c.Tag {
				continue
			}

			if !validVIPs[v.Name] {
				log.Infof("housekeeping: deleting obsolete VIP '%s'", v.Name)
				err := c.Fortigate.DeleteVIP(v.MKey())
				if err != nil {
					log.Errorf("error deleting VIP '%s': %s", v.Name, err.Error())
				}
			}
		}

		if !loop {
			return
		}
		time.Sleep(60 * time.Second)
	}
}
