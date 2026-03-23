# k8s-doctor

SRE-grade Kubernetes troubleshooting CLI. Zero config. Drop binary, run it.
Context switches on the fly. Same workflow as Kluster-bull.

---

## STEP 1 — Build the binary on your LOCAL machine

```bash
# Clone your source repo (do this once)
git clone https://github.com/YOUR_USERNAME/k8s-doctor
cd k8s-doctor

# Install Go dependencies
go mod tidy

# Build a static Linux binary (runs on the jumpserver)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o k8s-doctor .

# Verify it built
ls -lh k8s-doctor
```

---

## STEP 2 — Push binary to GitHub (same as Kluster-bull)

```bash
# Still on your local machine, inside the k8s-doctor folder
git add k8s-doctor
git commit -m "release: v0.1.0"
git push origin main
```

---

## STEP 3 — On the jumpserver (copy-paste exactly)

```bash
# Go to tmp
cd /tmp

# Clone your repo (first time only)
git clone https://github.com/YOUR_USERNAME/k8s-doctor

# Enter the directory
cd k8s-doctor

# Make executable
chmod +x k8s-doctor

# Run it
./k8s-doctor --help
```

---

## STEP 4 — Update binary on jumpserver (every time you push a new build)

```bash
cd /tmp/k8s-doctor
git pull
./k8s-doctor --help
```

---

## Commands — copy and paste these on the jumpserver

```bash
# List all available kube contexts
./k8s-doctor list

# TRIAGE — first stop for any ticket
./k8s-doctor triage --cluster prod-us-east-1
./k8s-doctor triage --cluster staging-eu-west-1 --namespace payments

# Fetch crash logs from a specific pod
./k8s-doctor triage logs my-app-pod-xyz -n payments --cluster prod-us-east-1

# NODE CHECKS
./k8s-doctor node pressure --cluster prod-us-east-1
./k8s-doctor node top      --cluster prod-us-east-1
./k8s-doctor node taints   --cluster prod-us-east-1

# Cordon a bad node
./k8s-doctor node cordon ip-10-0-1-55.ec2.internal --cluster prod-us-east-1

# Cordon AND drain
./k8s-doctor node cordon ip-10-0-1-55.ec2.internal --cluster prod-us-east-1 --drain

# NETWORK CHECKS
./k8s-doctor network dns     --cluster prod-us-east-1
./k8s-doctor network svc     my-service --cluster prod-us-east-1 --namespace payments
./k8s-doctor network netpol  --cluster prod-us-east-1 --namespace payments
./k8s-doctor network ingress --cluster prod-us-east-1

# AWS CHECKS
./k8s-doctor aws ec2 --cluster prod-us-east-1 --region us-east-1
./k8s-doctor aws alb --cluster prod-us-east-1 --region us-east-1
./k8s-doctor aws sg  --cluster prod-us-east-1 --region us-east-1
./k8s-doctor aws iam --cluster prod-us-east-1 --region us-east-1 --namespace payments
./k8s-doctor aws asg --cluster prod-us-east-1 --region us-east-1

# INCIDENT REPORT — paste output into Jira/ServiceNow
./k8s-doctor report --cluster prod-us-east-1 --ticket INC-1234

# Save report to a file
./k8s-doctor report --cluster prod-us-east-1 --ticket INC-1234 > /tmp/incident-report.md

# If region not in cluster name, pass it explicitly
./k8s-doctor triage --cluster my-cluster --region ap-southeast-1

# If using a specific AWS profile
./k8s-doctor triage --cluster prod-us-east-1 --profile prod-account

# Verbose mode (shows every command being run)
./k8s-doctor triage --cluster prod-us-east-1 --verbose

# JSON output (pipe to jq)
./k8s-doctor triage --cluster prod-us-east-1 --output json | jq .
```

---

## Context switching — how it works

The tool tries your existing kubeconfig contexts first (instant).
If not found, it runs `aws eks update-kubeconfig` automatically.

```bash
# These all work back to back — context flips each time
./k8s-doctor triage --cluster prod-us-east-1
./k8s-doctor triage --cluster staging-eu-west-1
./k8s-doctor node pressure --cluster prod-ap-southeast-1
```

Region is auto-detected from cluster name (e.g. `prod-us-east-1` → `us-east-1`).
Pass `--region` only if your cluster name does not contain the region.
