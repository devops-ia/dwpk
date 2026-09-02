# Documentation index

dwpk is a Kubernetes-hosted development workspace platform. A cluster admin curates `WorkspaceImage` catalog entries, provisions one `UserSpace` per person, and users create namespaced `Workspace` sessions that they reach through the SSH gateway or the web UI. This folder collects the user-facing reference set for operators, platform admins, and end users.

Start here:

- [Quick start](./QUICKSTART.md)
- [Architecture](./ARCHITECTURE.md)
- [Gateway on AWS](GATEWAY_AWS.md) - exposing SSH with the AWS Load Balancer Controller (NLB, not ALB)
- [Installation](./INSTALLATION.md)
- [Administration](./ADMINISTRATION.md)
- [User guide](./USER_GUIDE.md)
- [API reference](./API_REFERENCE.md)
- [Troubleshooting](./TROUBLESHOOTING.md)

Development notes stay in the root [README](https://github.com/devops-ia/dwpk/blob/main/README.md). This docs set does not duplicate that local dev loop.
