# Fleet context

Environment-specific patterns for this fleet: EKS and Rancher, with
Karpenter, VPC CNI, Portworx, and Artifactory. Use them to form
hypotheses, then confirm with read-only checks as usual.

## Operating environment
- Work runs from a Linux jump server with kubectl. There is **no AWS CLI and
  no jq** there, so AWS-side facts (EC2 status, ASG activity, subnet free
  IPs) must come from the AWS Console. When one is needed, say so in
  UNCONFIRMED / NEXT CHECKS and name the exact console page.
- `k8s-doctor aws *` subcommands shell out to the AWS CLI and will fail on
  the jump server. Don't use them there.

## Karpenter
- Pending pods plus NodeClaims stuck not Ready (`kubectl get nodeclaims`)
  point to a launch failure: instance type or capacity unavailable, a
  subnet or security group selector mismatch, or AMI/bootstrap problems.
  The `describe nodeclaim` conditions carry the reason.
- NodePool `limits` reached means no new nodes. Check with
  `kubectl get nodepools -o wide`.
- Spot interruption or consolidation causes node churn: a burst of pod
  restarts or reschedules with a node disappearing. That's expected churn
  unless PDBs are blocking or the workload lacks replicas.
- Nodes that join but never go Ready: check the Karpenter-initialized
  taints (`karpenter.sh/unregistered`) and the aws-node/CNI pods on that node.

## VPC CNI (aws-node)
- `FailedCreatePodSandBox` or "failed to assign an IP address" means the
  node or subnet ran out of IPs, or aws-node is unhealthy. Check the
  `aws-node` pod on that node (`kubectl -n kube-system get pods -o wide |
  grep aws-node`) and its logs. Subnet free-IP counts need the AWS Console.
- After CNI add-on upgrades, check the aws-node DaemonSet rollout state first.

## Portworx
- `FailedAttachVolume` / `FailedMount` on Portworx volumes: check whether
  the volume is still attached to another (possibly dead) node, and the
  health of the Portworx pods (`kubectl -n portworx get pods -o wide`, or
  `kube-system` depending on the install).
- Force-deleting a pod with a Portworx volume can leave the volume
  attached. Always call that out when a remedy suggests `--force`.

## Artifactory (image pulls)
- Most images are pulled from Artifactory, so pull secrets are
  namespace-provisioned credentials. A secret missing in one namespace
  points to a provisioning or rotation gap for that namespace. The same
  secret missing in many namespaces points to a failed rotation job
  (correlation rule 4).
- `unauthorized` / `401` in pull events means an expired or rotated token.
  `manifest unknown` means a bad tag or the wrong repo path. Timeouts
  point to the network or proxy, or Artifactory itself: check the other
  namespaces pulling from the same registry before blaming the app.

## AL2 to AL2023 nodes
- AL2023 uses cgroup v2. Old JVMs, older monitoring agents, and anything
  that reads `/sys/fs/cgroup/memory/` directly can misreport memory or
  crash on AL2023 nodes. If failures cluster on AL2023 nodes only (check
  `node pressure` `ami_family` or node labels), treat the cgroup version
  as a hypothesis.
