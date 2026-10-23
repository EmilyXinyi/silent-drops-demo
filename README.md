# The Silent Drops — demo application

A proof-of-concept for the talk **The Silent Drops: Making Argo CD Network-Aware with eBPF and Cilium Hubble**.

## What is included

- `apps/backend`: tiny HTTP backend.
- `apps/frontend`: HTTP endpoint that calls the backend and reports success/failure.
- `deploy/`: namespace, apps, NetworkPolicy, `NetworkHealth` CRD and sample custom resource.
- `controller/`: Go controller that watches Hubble dropped-flow events and updates `NetworkHealth` status.
- `argocd/`: Argo CD Lua health customization and example Application.
- `scripts/`: helper commands for the demo.

## Important status

This is a **proof-of-concept starter**, not a production-ready controller. It uses a deliberately simple matching rule: a dropped flow involving the configured source and destination pods marks the resource Degraded. Before using it for production, add stronger flow correlation, policy-denial filtering, rate/window semantics, metrics, leader election, retries, and tests.

The controller uses the Hubble Relay gRPC API. Hubble Relay TLS settings vary by installation. The sample defaults to an insecure gRPC connection to the in-cluster Relay service, which is suitable only for a controlled local demo if Relay is configured for plaintext. If your Relay endpoint requires TLS, configure gRPC TLS credentials and mount the appropriate CA/client credentials rather than disabling TLS.

## Prerequisites

- Docker
- `kind`
- `kubectl`
- `helm`
- `cilium` CLI
- Go 1.23+
- Argo CD CLI (optional)

Use a fresh local cluster for the demo.

## 1. Create a cluster and install Cilium + Hubble

```bash
kind create cluster --name silent-drops
cilium install
cilium hubble enable
cilium status --wait
```

Verify Hubble is running and inspect flows:

```bash
cilium hubble port-forward &
hubble observe --last 10
```

If your installed Cilium CLI uses a different port-forward workflow, follow the CLI output/help for your version.

## 2. Install Argo CD

```bash
kubectl create namespace argocd
kubectl apply -n argocd --server-side --force-conflicts -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# checking if Argo CD is working properly
kubectl port-forward svc/argocd-server -n argocd 8080:443
argocd admin initial-password -n argocd
argocd login <ARGOCD_SERVER>

```

Configure the custom health check:

```bash
kubectl patch configmap argocd-cm -n argocd --type merge --patch-file argocd/argocd-cm.yaml
# kubectl apply --server-side --field-manager=silent-drops -f argocd/argocd-cm.yaml
kubectl rollout restart statefulset/argocd-application-controller -n argocd
```

Note: if `argocd-cm` already exists, merge this data key into it rather than replacing the ConfigMap. The sample ConfigMap contains only the customization.

## 3. Deploy the app and NetworkHealth resource

```bash
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/backend.yaml
kubectl apply -f deploy/frontend.yaml
kubectl apply -f deploy/networkhealth-crd.yaml
kubectl apply -f deploy/networkhealth.yaml
kubectl apply -f demo/network-policy-allow.yaml
```

Wait for the workloads:

```bash
kubectl -n silent-drops rollout status deploy/backend
kubectl -n silent-drops rollout status deploy/frontend
kubectl -n silent-drops get pods
```

Port-forward the frontend and test it:

```bash
kubectl -n silent-drops port-forward svc/frontend 8090:8080
curl -i http://localhost:8080/
```

The frontend should return HTTP 200 when the backend is reachable.

## 4. Build and run the controller

Build locally:

```bash
cd controller
go mod tidy
go build -o network-health-controller .
```

The controller needs access to Kubernetes and Hubble Relay. For a local process, use your current kubeconfig and port-forward Relay to the expected local port. For an in-cluster deployment, use the included RBAC and Deployment manifests.

The sample controller defaults:
- Hubble Relay target: `hubble-relay.kube-system.svc:80`
- Namespace: `silent-drops`
- Resource: `networkhealths.demo.io`
- Name: `frontend-backend`
- Source pod prefix: `frontend-`
- Destination pod prefix: `backend-`

Environment variables can override these values. See `controller/main.go`.

### In-cluster controller image

Build an image and load it into kind:

```bash
docker build -t silent-drops-controller:dev -f controller/Dockerfile .
kind load docker-image silent-drops-controller:dev --name silent-drops
kubectl apply -f deploy/controller-rbac.yaml
kubectl apply -f deploy/controller-deployment.yaml
```

Before applying the Deployment, verify the Hubble Relay service port and TLS mode in your installation. Update `HUBBLE_RELAY_ADDR` and the gRPC transport setup if needed.

## 5. Add the custom resource to Argo CD

For a quick local demo, create an Argo CD Application that includes the `NetworkHealth` resource. A Git repository is recommended for a real GitOps demonstration. `argocd/application.yaml` is an example; replace the repository URL and path with your own repo.

Argo CD's health customization is keyed by the exact GVK:
`resource.customizations.health.demo.io_NetworkHealth`.

The Application must manage the `NetworkHealth` object for its health to contribute to the Application tree.

## 6. Break the network

Apply a policy that selects the frontend but does not allow it to reach the backend:

```bash
kubectl apply -f demo/network-policy-block.yaml
```

Generate requests in another terminal:

```bash
while true; do
  curl -sS -o /dev/null -w '%{http_code}\n' http://localhost:8090/ || true
  sleep 1
done
```

Observe dropped traffic:

```bash
kubectl -n kube-system exec ds/cilium -c cilium-agent -- \
  hubble observe --verdict DROPPED --namespace silent-drops -f
```

Expected result: the frontend requests fail, Hubble reports relevant dropped flows, the controller sets `NetworkHealth.status.healthy=false`, and Argo CD marks the custom resource Degraded.

Restore connectivity:

```bash
kubectl apply -f demo/network-policy-allow.yaml
```

The sample controller restores Healthy after its recovery window. If it does not, inspect controller logs and Hubble Relay connectivity.

## Demo safety and accuracy

- A dropped packet is not automatically an outage. The POC matches configured pod prefixes and uses a simple recovery timer.
- This sample does not yet prove that every matching drop was caused specifically by a NetworkPolicy; Cilium drop verdicts can have different causes.
- The `NetworkHealth` status is runtime state and should be written by the controller, not committed as a static status field in Git.
- Never run this controller with broad cluster write permissions in production.
