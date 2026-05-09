# bamboo-controller Helm chart

Helm chart for the bamboo control-plane container. Deployed by the
`tokyo-dev` Terraform composition into the EKS cluster from the
`infra/terraform/modules/eks` module.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Manual install (operator path)

```bash
aws eks update-kubeconfig --region ap-northeast-1 --name tokyo-dev-eks
kubectl create namespace bamboo

# Generate the env-specific config secret. The Terraform module
# stores the secret ARNs; helm reads the underlying values via
# External Secrets (separate install) OR you can mirror them as a
# Kubernetes Secret manually for first-time bring-up:
kubectl create secret generic tokyo-dev-config -n bamboo \
    --from-literal=DATABASE_URL=$(aws secretsmanager get-secret-value \
        --secret-id tokyo-dev/bamboo/database_url --query SecretString --output text) \
    --from-literal=CLICKHOUSE_URL=$(aws secretsmanager get-secret-value \
        --secret-id tokyo-dev/bamboo/clickhouse_url --query SecretString --output text) \
    --from-literal=BAMBOO_SESSION_SECRET=$(aws secretsmanager get-secret-value \
        --secret-id tokyo-dev/bamboo/session_secret --query SecretString --output text) \
    --from-literal=BAMBOO_BASE_URL=https://bamboo.example.com

helm install tokyo-dev infra/helm/bamboo-controller -n bamboo \
    -f infra/helm/bamboo-controller/values.tokyo-dev.yaml
```

## What it deploys

| Resource    | Notes                                              |
| ----------- | -------------------------------------------------- |
| Deployment  | 2 controller pods reading config from a Secret    |
| Service     | ClusterIP exposing http (80) + grpc               |
| ServiceAccount | IRSA-bound for Secrets Manager access (when role ARN supplied) |
| Ingress     | ALB with HTTPS:443; auto-discovers ACM cert via host |

## What's missing (deferred)

- ALB controller and External Secrets operator are not installed by
  this chart; install separately via `aws-load-balancer-controller`
  and `external-secrets-operator` Helm releases.
- The controller image's command-line flag handling needs to learn
  to read DATABASE_URL etc. from env vars before the manual-Secret
  bring-up works end-to-end. Tracked as the controller-env-vars
  refactor (separate Phase 2 item).
- TLS cert auto-issuance via cert-manager + ACM is a follow-up.
