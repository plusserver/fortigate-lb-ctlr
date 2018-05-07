# fortigate-lb-ctlr

fortigate-lb-ctlr configures a Fortigate appliance to provide loadbalancing for Kubernetes Services.

## Overview

fortigate-lb-ctlr creates a virtual server for every Kubernetes Service that needs external loadbalancing.
This works similar to a Service with type `Loadbalancer`. Every service will get its own IP address picked from an
address range that is managed by the controller.

## Getting started

First, choose a network for your virtual servers and make sure it is routed to your Fortigate.

Then, deploy a service to manage the IP address reservation for the virtual servers. Right now there are:

 * https://github.com/Nexinto/k8s-ipam-configmap (use this by default)
 * https://github.com/Nexinto/k8s-ipam-haci

When deploying one of those, one of the required configuration parameters is the network you are going to use for
your virtual services.

(Or, just deploy the custom resource from https://github.com/Nexinto/k8s-ipam/deploy/crd.yaml and manually provide
IP addresses by editing the request objects, but that's not recommended.)

To run fortigate-lb-ctlr in your cluster:

- create a secret `fortigate-lb-ctlr` in namespace `kube-system` with your required credentials, for example:

```bash
kubectl create secret generic fortigate-lb-ctlr \
--from-literal=FORTIGATE_USER=... \
--from-literal=FORTIGATE_PASSWORD=... \
--from-literal=FORTIGATE_API_KEY=... \
-n kube-system
```

Depending on your Fortigate setup, use either `FORTIGATE_USER` and `FORTIGATE_PASSWORD` or `FORTIGATE_API_KEY`.
The controller will try to use the API key first.

- review and modify `deploy/configmap.yaml`. `FORTIGATE_URL` is required. See below for Then, create the configmap:

```bash
kubectl apply -f deploy/configmap.yaml
```

- apply the RBAC configuration:

```bash
kubectl apply -f deploy/rbac.yaml
```

- deploy the controller

```bash
kubectl apply -f deploy/deployment.yaml
```

You can also run the controller outside the Kubernetes cluster by setting the required environment variables.

## Configuration parameters

All configuration parameters are passed to the controller as environment variables:

| Variable | Description | Default |
|:-----|:------------|:--------|
|KUBECONFIG|your kubeconfig location (out of cluster only)||
|LOG_LEVEL|log level (debug, info, ...)|info|
|FORTIGATE_URL|URL of the Fortigate API||
|FORTIGATE_API_KEY|Fortigate API key||
|FORTIGATE_USER|Fortigate API user||
|FORTIGATE_PASSWORD|Fortigate API user password||
|FORTIGATE_REALSERVER_LIMIT|Maximum number of Fortigate Realservers (required by some Fortigate license types)|""|
|FORTIGATE_MANAGE_POLICY|Also manage a Fortigate NAT policy for each Virtual Server if your setup requires this|false|
|FORTIGATE_DEBUG||false|
|REQUIRE_TAG|Create loadbalancing only for Services with the annotation `nexinto.com/req-vip`|false|
|CONTROLLER_TAG|All Fortigate resources created by fortigate-lb-ctlr are tagged with this. Set to a unique value if you are running multiple controller instances|kubernetes|

## How to use it

By default, loadbalancing is created for every Service with type `NodePort`. If everything works, the 
virtual address created for the Service is added as an annotation `nexinto.com/vip`:

```bash
kubectl describe service myservice
...
Annotations: ...
              nexinto.com/vip=10.160.10.161

```
(If you do not need loadbalancing for every service (of type `NodePort`), you can start the controller
with the configuration parameter `REQUIRE_TAG=true`. The controller will not create a virtual server
by default. Then, set the annotation `nexinto.com/req-vip` on all Services that require loadbalancing to `true`.)

The addresses will be picked from the network you configured when you deployed one of the IP address
management services. If you create a new Service that needs to be loadbalanced, fortigate-lb-ctlr will
create a new `ipaddress` request resource and waits for it to be processed. It then creates a new
virtual server using that IP address.

You can list those addresses using `kubectl get ipaddresses` and check details with `kubectl describe ipaddress ...`

## Troubleshooting

If your virtual server isn't created, first check the Events for your Service (`kubectl describe service ...`)
and for the IP address resource (`kubcetl describe ipaddress ...`; the name for the address is the same as your service).
Then, check the logs of the controller:

```bash
kubectl logs -f $(kubectl get pods -l app=fortigate-lb-ctlr -n kube-system -o jsonpath='{.items[0].metadata.name}') -n kube-system
```

and the logs of the controller that provides the IP addresses.
