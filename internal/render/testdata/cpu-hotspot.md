# k8s-doctor report: prod-api-apac

Captured Tue, 06 Oct 2026 07:45:00 UTC · namespaces: all · read-only scan.
Usage figures are one metrics-server sample: confirm sizing against p95/peak history before applying.

**Summary:** 1 workload to resize  ·  1 CPU hotspot

## Workloads to resize, by namespace

### `storefront-prod`

| Workload | Uses | Requests | Fix in | Proposed | Note |
|---|---|---|---|---|---|
| web-api | 1.6Gi + 2.5 cores | none + none | Deployment | requests: {cpu: 1900m, memory: 2Gi}, limits: {memory: 2Gi} | BestEffort |

How to apply:

- **web-api**: Set requests (and a memory limit equal to the request: memory is not compressible) on the workload: `kubectl set resources deployment/web-api -n storefront-prod --requests=cpu=1900m,memory=2Gi --limits=memory=2Gi`

## CPU hotspots

| Namespace | Workload | Busiest replica | Of its node | Request | Scaling |
|---|---|---|---|---|---|
| `storefront-prod` | web-api | 2.5 cores | 64% | none | no HPA · 2.1x uneven · 64 restarts |

Next steps:

- **web-api**: Add an HPA so the workload scales out under load (after CPU requests are set): `kubectl autoscale deployment web-api -n storefront-prod --cpu-percent=70 --min=2 --max=6`
