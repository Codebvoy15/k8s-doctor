# k8s-doctor report: dev-data-platform

Captured Tue, 06 Oct 2026 07:45:00 UTC · namespaces: all · read-only scan.
Usage figures are one metrics-server sample: confirm sizing against p95/peak history before applying.

**Summary:** 2 nodes ≥85% memory  ·  8 workloads to resize  ·  1 degraded

## Findings

| Tier | Finding |
|---|---|
| DEGRADED | Deployment orders-cdc/cdc-orders: chronic OOMKills (50 restarts) |

## Nodes under pressure

| Node | Resource | Used | Requested | Caused by |
|---|---|---|---|---|
| `ip-10-0-1-11.ec2.internal` | memory | 59.1Gi (100%) | 24.5Gi (41%) | ksqldb-cluster-1, dev-connect-1, test-connect-1, kraftcontroller |
| `ip-10-0-1-12.ec2.internal` | memory | 50.3Gi (85%) | 10.3Gi (17%) | controlcenter, ksqldb-cluster-1, platform-prometheus-server |

## Workloads to resize, by namespace

### `confluent`

| Workload | Uses | Requests | Fix in | Proposed | Note |
|---|---|---|---|---|---|
| ksqldb-cluster-1 | 14.6Gi | none | KsqlDB CR ksqldb-cluster-1 | requests: {memory: 17920Mi}, limits: {memory: 17920Mi} | drives ip-10-0-1-11, ip-10-0-1-12 · no requests on app containers |
| controlcenter | 23.1Gi | none | ControlCenter CR controlcenter | requests: {memory: 28416Mi}, limits: {memory: 28416Mi} | drives ip-10-0-1-12 · no requests on app containers |
| dev-connect-1 | 12.4Gi | none | Connect CR dev-connect-1 | requests: {memory: 15Gi}, limits: {memory: 15Gi} | drives ip-10-0-1-11 · no requests on app containers |
| test-connect-1 | 10.9Gi | none | Connect CR test-connect-1 | requests: {memory: 13568Mi}, limits: {memory: 13568Mi} | drives ip-10-0-1-11 · no requests on app containers |
| kraftcontroller | 8.1Gi | none | KRaftController CR kraftcontroller | requests: {memory: 9984Mi}, limits: {memory: 9984Mi} | drives ip-10-0-1-11 · no requests on app containers |
| kafka | 23.8Gi | none | Kafka CR kafka | requests: {memory: 29440Mi}, limits: {memory: 29440Mi} | no requests on app containers |

How to apply:

- **ksqldb-cluster-1**: Set the resources in the Confluent for Kubernetes KsqlDB CR "ksqldb-cluster-1" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit ksqldb ksqldb-cluster-1 -n confluent`
- **controlcenter**: Set the resources in the Confluent for Kubernetes ControlCenter CR "controlcenter" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit controlcenter controlcenter -n confluent`
- **dev-connect-1**: Set the resources in the Confluent for Kubernetes Connect CR "dev-connect-1" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit connect dev-connect-1 -n confluent`
- **test-connect-1**: Set the resources in the Confluent for Kubernetes Connect CR "test-connect-1" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit connect test-connect-1 -n confluent`
- **kraftcontroller**: Set the resources in the Confluent for Kubernetes KRaftController CR "kraftcontroller" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit kraftcontroller kraftcontroller -n confluent`
- **kafka**: Set the resources in the Confluent for Kubernetes Kafka CR "kafka" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit kafka kafka -n confluent`

### `kube-system`

| Workload | Uses | Requests | Fix in | Proposed | Note |
|---|---|---|---|---|---|
| platform-prometheus-server | 2.5Gi | none | Helm release platform-prometheus | requests: {memory: 3Gi}, limits: {memory: 3Gi} | drives ip-10-0-1-12 · BestEffort |

How to apply:

- **platform-prometheus-server**: Set the resources in the release's values and roll out with helm upgrade (a kubectl edit is overwritten on the next upgrade)

### `ingest-dev`

| Workload | Uses | Requests | Fix in | Proposed | Note |
|---|---|---|---|---|---|
| ingest-gateway | 8.6Gi | 4.0Gi | Deployment | requests: {memory: 10752Mi}, limits: {memory: 10752Mi} |  |

How to apply:

- **ingest-gateway**: Set requests (and a memory limit equal to the request: memory is not compressible) on the workload: `kubectl set resources deployment/ingest-gateway -n ingest-dev --requests=memory=10752Mi --limits=memory=10752Mi`

_5 lower-tier findings not shown (run with `--min-tier info`)._
