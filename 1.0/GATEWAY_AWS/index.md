# The SSH gateway on AWS

How to expose the dwpk gateway on EKS with the AWS Load Balancer Controller.

## An ALB cannot carry SSH

Start here, because it saves an afternoon.

An **Application Load Balancer is HTTP and HTTPS only**. It parses requests, it routes on hostnames and paths, and it has no notion of a raw TCP stream. SSH is not HTTP, so an ALB cannot carry it - not with an annotation, not with a listener rule, not at all.

The gateway needs a **Network Load Balancer**, which is layer 4 and forwards TCP untouched. The AWS Load Balancer Controller provisions both kinds, so the controller in the request is the right one; it is the load balancer type that has to differ.

The split for a dwpk install is:

| Component | Protocol     | Load balancer | Kubernetes object             |
| --------- | ------------ | ------------- | ----------------------------- |
| Gateway   | SSH (TCP/22) | **NLB**       | `Service` type `LoadBalancer` |
| UI        | HTTPS        | **ALB**       | `Ingress`                     |

## The gateway Service

The chart creates a `Service` of type `LoadBalancer` on port 22. On EKS the controller turns that into an NLB when you ask it to:

```
gateway:
  service:
    type: LoadBalancer
    annotations:
      # Hand this Service to the AWS Load Balancer Controller rather than to the
      # legacy in-tree cloud provider. Without it you may still get a load
      # balancer - the old classic one - and none of the annotations below.
      service.beta.kubernetes.io/aws-load-balancer-type: external
      service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
      service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing
      # Health checks. The gateway speaks SSH, so a TCP check is the only
      # meaningful one; see "Health checks and log noise" below.
      service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol: tcp
      service.beta.kubernetes.io/aws-load-balancer-healthcheck-port: "22"
```

**`nlb-target-type: ip`** sends traffic straight to pod IPs rather than through a node port. It removes a hop, and it keeps the source address intact without `externalTrafficPolicy: Local` and the scheduling constraints that brings.

**`scheme`** is `internet-facing` or `internal`. Internal is the right default if people reach the cluster over a VPN or Direct Connect: an SSH endpoint on the public internet will be found by scanners within minutes, and while it only accepts public keys, there is no reason to advertise it.

## Health checks and log noise

An NLB health check opens a TCP connection to port 22, decides the target is healthy because the connection succeeded, and closes it without speaking SSH. The kubelet's own `tcpSocket` probe does the same.

The gateway logs each of these at **debug**:

```
DEBUG gateway SSH handshake did not complete {"client": "10.0.1.23:41234", "reason": "EOF"}
```

That line is expected, and there will be one per health-checking source per interval - the NLB from each subnet, plus the kubelet. It used to be logged at error, which made a perfectly healthy gateway look broken. A genuine authentication failure is logged separately and stays visible at a normal level.

If you want the noise gone entirely, raise the health check interval; do not raise the log level, or you lose real failures with it.

## Idle timeout

NLB idle timeout for TCP flows is **350 seconds and cannot be changed**. An SSH session sitting at a prompt sends nothing, so it will be cut after roughly six idle minutes unless something keeps the flow alive.

Two ways to handle it, and you want at least one:

- **Client side**, in `~/.ssh/config` - the usual answer:

```
Host *.dwpk.example.com
  ServerAliveInterval 60
  ServerAliveCountMax 3
```

- **Server side**, by enabling TCP keepalives on the gateway Service:

```
service.beta.kubernetes.io/aws-load-balancer-target-group-attributes: |
  preserve_client_ip.enabled=true
```

A dropped session loses the terminal, not the workspace: the pod keeps running and reconnecting picks up where the shell left off - with the caveat that anything running in the foreground of that shell dies with it. `tmux` inside the workspace is worth the habit.

## Security groups

With `nlb-target-type: ip`, traffic arrives at the pod ENI, so the **node's** security group must allow TCP/22 from wherever you decided `scheme` puts the listener - the VPC CIDR for `internal`, or the world for `internet-facing`.

The controller can manage this for you:

```
service.beta.kubernetes.io/aws-load-balancer-manage-backend-security-group-rules: "true"
```

Leave it off and you must open the rule yourself, and the symptom is a connection that hangs rather than one that is refused.

## Client IP

`preserve_client_ip.enabled=true` on the target group keeps the real source address, which is what makes the gateway's connection logs worth reading. It is on by default for `ip` targets and off for `instance` targets.

It has one consequence worth knowing: with client IP preserved, a pod cannot reach the load balancer that fronts its own Service - the packet arrives with a source address it then tries to reply to directly. Nothing in dwpk does that, but a debugging session that tries `ssh` from inside a workspace to the platform's own endpoint will hang, and this is why.

## DNS

Point a record at the load balancer and use it as the SSH host:

```
*.dwpk.example.com  CNAME  k8s-dwpksys-dwpkgate-abc123.elb.eu-west-1.amazonaws.com
```

The gateway identifies a workspace from the **SSH username**, not the hostname, so a single record serves everyone:

```
ssh alice-dev@dwpk.example.com
```

That username is `<user>-<workspace>`. It carries the owner because a workspace name is unique only inside its namespace - two people can each have one called `dev`, and the gateway matches across the whole cluster. `status.endpoint` on each Workspace publishes the exact string, which is what the UI shows and what the VS Code deep link uses.

## Checking it works

```
# The load balancer exists and has an address.
kubectl get svc -n dwpk-system dwpk-gateway-service

# Targets are healthy. Unhealthy here with a Running pod is almost always the
# security group.
aws elbv2 describe-target-health --target-group-arn <arn>

# End to end, with the key on the Workspace.
ssh -v alice-dev@dwpk.example.com
```

`ssh -v` is worth the habit: it distinguishes a network problem (no banner) from an authentication one (banner, then `Permission denied (publickey)`), and those have entirely different causes.

## The UI, which does belong behind an ALB

The UI is ordinary HTTPS, so it goes through an Ingress:

```
ui:
  ingress:
    enabled: true
    className: alb
    annotations:
      alb.ingress.kubernetes.io/scheme: internet-facing
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:...
      alb.ingress.kubernetes.io/ssl-redirect: "443"
```

Serve it over HTTPS and leave `ui.cookieSecure` at its default of `true`. The session cookie is the credential for a browser session; over plain HTTP it travels in clear, and the browser will not treat it as secure. Local development against `http://localhost` is the only place `cookieSecure: false` belongs.

The UI's WebSocket endpoints - the browser terminal - work through an ALB without extra configuration; ALB has supported WebSocket upgrades since it shipped. Raise the ALB idle timeout above its 60-second default if you want long terminal sessions to survive being left alone:

```
alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
```
