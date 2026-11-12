# FAQ

**Why does `oai-chat-apiserver` refuse to start without `controlplane.kubeconfig`?**
Command grants and peer bindings are declarative resources. A second SQL reader would be
a second source of truth, so the control plane is required. Duty groups are now an
imperative chat-owned resource backed by PostgreSQL and are no longer read from the
control plane.

**Can I run without a duty backend?**
Yes. Leave `duty.provider` unset (or `none`) and do not configure
`backend.regen-endpoint`. The duty store stays empty and tickets stay local.

**PostgreSQL or MySQL?**
PostgreSQL is the primary path (three schemas). MySQL works with
`coredb.dialect: mysql`; table names get a service prefix.

**Which ports are used?**
The defaults are in `configs/examples/`: agent apiserver `:8080`, chat apiserver `:8082`
(with the MCP listener on `:8091` in the chart), MCP server `:8080`, declserver `:6444`.

**Where do secrets go?**
Never in git. Use environment variables (`OAI_<SERVICE>_<PATH>`), the Helm chart's
Kubernetes Secrets, or your secret manager.

**Why is the `onexai`/`harness` CLI not here?**
The CLI is a separate private tool. This repository intentionally contains only the
server platform and its public packages.

**A tool reports "requires SRE role"**
That comes from the tool's own authorization check. Configure the role lists and command
grants as declarative resources; an empty list means nobody has the role.
