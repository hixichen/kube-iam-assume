# kube-iam-assume

**Your self-hosted Kubernetes cluster deserves the same cloud access as EKS.**

EKS, GKE, and AKS all let workloads call AWS/GCP/Azure APIs without a single secret — short-lived, auto-rotating, cryptographically bound to a specific service account. Your kubeadm, k3s, RKE2, Talos, or bare-metal cluster can have exactly the same capability in under five minutes.

One controller. One config change. Zero secrets.

---

## The Credential Problem Is Worse Than You Think

Ask any platform team how their self-hosted K8s workloads authenticate to cloud services. The answer is almost always some version of this:

- An IAM access key in a Kubernetes Secret, probably copied from a developer's laptop
- An EC2/GCE instance profile that gives every pod on every node the same identity
- A service account key JSON file checked into a Helm values file in a private git repo — where "private" is doing a lot of work
- Credentials that were supposed to be rotated quarterly and weren't

Static credentials don't expire. They get copied into CI pipelines. They get committed to git. They get printed in log files. The average credential leak takes [207 days to detect](https://www.ibm.com/reports/data-breach). The damage is real: S3 data exfiltration, cryptocurrency mining, lateral movement into production databases.

The managed Kubernetes services solved this years ago. You shouldn't have to wait for a migration to EKS.

---

## What kube-iam-assume Does

Your cluster already issues OIDC-compliant JWT tokens for every service account. AWS, GCP, and Azure already accept those tokens via `AssumeRoleWithWebIdentity`, Workload Identity Federation, and Federated Credentials respectively. The only reason this doesn't work for self-hosted clusters is that the JWKS endpoint — the public keys cloud providers need to verify the tokens — is private.

That's it. That's the only gap.

kube-iam-assume fills exactly that gap and nothing else. It:

1. Reads your cluster's public signing keys from the API server (in-cluster, no internet access required)
2. Publishes them to a public S3/GCS/Azure Blob bucket
3. Keeps them in sync automatically — including through key rotations

Your workloads get the same flow as EKS IRSA:

```
Pod starts
  → K8s mounts a short-lived projected service account token
  → App calls AWS SDK as normal
  → SDK reads the token, calls sts:AssumeRoleWithWebIdentity
  → AWS fetches your published JWKS, verifies the token signature
  → AWS returns 1-hour temporary credentials, scoped to exactly one IAM role
  → Your app runs. No secrets. Nothing to rotate. Nothing to leak.
```

No code changes. No sidecars. No agents on nodes. No service mesh. No PKI infrastructure.

---

## Who This Is For

**If you run self-hosted Kubernetes and your workloads call cloud APIs, this is for you.**

The problem is particularly acute for:

- **GPU and AI infrastructure teams** building bare-metal clusters for training and inference — you need S3 for model storage and datasets from day one, and standing up SPIRE is the last thing you want to do at 2am before a training run
- **Platform teams on kubeadm/k3s/RKE2/Talos** who want feature parity with managed Kubernetes without the managed Kubernetes price tag
- **Multi-cloud shops** where workloads need simultaneous access to AWS, GCP, and Azure — one installation handles all three
- **Security-conscious teams** who have been meaning to eliminate static credentials for months but couldn't justify the SPIRE learning curve

If you run **EKS, GKE, or AKS**, you don't need this. Your managed service already does it natively.

---

## What You Get

**No more long-lived secrets.** Credentials are issued on demand, expire in one hour, and are cryptographically bound to a specific Kubernetes service account. There is nothing to store, rotate, audit, or accidentally commit to git.

**Least-privilege by default.** Each IAM role is bound to one service account. The trust policy is explicit: "only the `payments` service account in the `production` namespace can assume this role." A compromised pod cannot escalate to other roles or other namespaces.

**Automatic key rotation.** When your API server rotates its signing keys, kube-iam-assume detects the change and publishes both old and new keys simultaneously for 24 hours. Zero-downtime, no manual intervention, no 3am pages.

**Fleet mode, one IAM policy.** Opt-in fleet mode lets a group of clusters share one issuer URL and one aggregated JWKS endpoint. One IAM trust policy covers every cluster in the fleet. Add a new cluster to the fleet — no cloud-side changes needed.

**Five-minute installation.** One `--service-account-issuer` flag on the API server, one `helm install`, one CLI call to register the OIDC provider. That's the full installation.

---

## Out of Scope

kube-iam-assume is intentionally narrow. Understanding what it does not do is as important as understanding what it does.

### Node-level and pod-level attestation

kube-iam-assume does not verify *where* a workload is running. It cannot attest that a token was produced by a specific node, a specific container image, or a specific process. The identity it asserts is: "this token was issued by Kubernetes for service account `<namespace>/<name>`." Nothing more.

If your threat model requires knowing that a credential was issued to a workload running on a specific node, a specific AMI, or a specific container image digest, you need node attestation — which is what SPIFFE/SPIRE provides via its SPIRE Agent.

### Access control to nodes or pods

kube-iam-assume has no mechanism to control which pods can or cannot assume cloud roles, beyond what the Kubernetes RBAC model already provides. It does not add admission control, does not validate container images before issuing credentials, and does not enforce any policy between "pod is running" and "cloud SDK acquires credentials."

The enforcement point is the **cloud IAM trust policy** — which service account can assume which role. If you need finer-grained control (e.g., preventing a pod from assuming a role even if it has the right service account), that requires a separate policy layer. The v0.3 `CloudIdentityBinding` CRD will introduce declarative controls on top of this, but it is not in scope for v0.1.

### Cross-platform or non-Kubernetes identity

kube-iam-assume only works for workloads running inside Kubernetes. It cannot issue credentials for VMs, bare-metal processes, CI/CD runners, or anything that doesn't have a Kubernetes service account token. For heterogeneous environments, use SPIFFE/SPIRE.

### Per-pod identity (AWS and Azure)

On AWS and Azure, the cloud IAM trust policy can only condition on the `sub` claim, which is `system:serviceaccount:<namespace>/<name>`. Two pods sharing a service account are indistinguishable. GCP is an exception — kube-iam-assume maps pod-level claims from the token as Workload Identity Federation attributes, enabling conditions that target a specific pod name or pod UID.

---

## kube-iam-assume vs SPIFFE/SPIRE

These tools solve adjacent problems at different layers. Understanding the difference prevents picking the wrong one.

|  | kube-iam-assume | SPIFFE/SPIRE |
|---|---|---|
| **What it solves** | Cloud IAM federation for K8s workloads | Universal workload identity across any platform |
| **Setup time** | ~5 minutes | Hours to days |
| **Infrastructure overhead** | One Deployment | SPIRE Server + SPIRE Agent on every node |
| **Identity scope** | Service account | Pod, process, VM, container, bare-metal |
| **Node/image attestation** | No | Yes |
| **mTLS / SVIDs** | No | Yes |
| **Non-Kubernetes workloads** | No | Yes |
| **Cloud IAM federation** | Native (AWS, GCP, Azure) | Via OIDC federation plugin |
| **Operational burden** | Minimal | Significant — agents, registration entries, trust bundles |

**Use kube-iam-assume when** you run self-hosted Kubernetes, your workloads need to access cloud APIs, and you want the simplest possible path to eliminating static credentials. It covers the 80% case with 5% of the operational effort.

**Use SPIRE when** you need per-pod or per-process identity, node attestation, mTLS between services, identity for non-Kubernetes workloads, or cross-platform identity federation. SPIRE is the right foundation for a comprehensive zero-trust infrastructure.

**They are not mutually exclusive.** SPIRE can federate with cloud IAM via OIDC — if you already run SPIRE, you probably don't need kube-iam-assume. If you don't run SPIRE, kube-iam-assume gets you to secretless cloud access today without building out the SPIRE infrastructure.

---

## Quick Start

### Prerequisites

- Kubernetes 1.22+
- Ability to modify the API server `--service-account-issuer` flag (one-time change)
- A publicly readable S3/GCS/Azure Blob bucket

### Step 1: Set the API Server Issuer

Add the bucket URL as the primary issuer. Keep the existing value as a secondary so tokens issued before the change remain valid.

```
--service-account-issuer=https://my-cluster-oidc.s3.us-west-2.amazonaws.com
--service-account-issuer=https://kubernetes.default.svc.cluster.local
```

See [ARCHITECTURE.md — Distribution-Specific Guidance](ARCHITECTURE.md#distribution-specific-guidance) for kubeadm, k3s, RKE2, Talos, minikube, and kind.

### Step 2: Install the Controller

```bash
helm install kube-iam-assume kube-iam-assume/kube-iam-assume \
  --set publisher.type=s3 \
  --set publisher.s3.bucket=my-cluster-oidc \
  --set publisher.s3.region=us-west-2
```

### Step 3: Register the OIDC Provider

```bash
kube-iam-assume setup aws \
  --issuer-url https://my-cluster-oidc.s3.us-west-2.amazonaws.com
```

One-time operation. This wraps the AWS CLI calls to create an IAM OIDC Identity Provider.

### Step 4: Write a Trust Policy and Deploy

```json
{
  "Effect": "Allow",
  "Principal": { "Federated": "arn:aws:iam::ACCOUNT:oidc-provider/my-cluster-oidc.s3.us-west-2.amazonaws.com" },
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": {
    "StringEquals": {
      "my-cluster-oidc.s3.us-west-2.amazonaws.com:sub": "system:serviceaccount:production:payments"
    }
  }
}
```

```yaml
volumes:
- projected:
    sources:
    - serviceAccountToken:
        audience: sts.amazonaws.com
        expirationSeconds: 3600
        path: token
```

The AWS SDK detects `AWS_ROLE_ARN` + `AWS_WEB_IDENTITY_TOKEN_FILE` and handles everything else. Your application code is unchanged.

---

## Multi-Cloud and Beyond

A single kube-iam-assume installation supports AWS, GCP, Azure, and any other OIDC consumer simultaneously. The metadata it publishes is cloud-agnostic — only the consumer-side registration differs.

| Consumer | Mechanism | CLI |
|---|---|---|
| AWS | IAM OIDC Provider + `AssumeRoleWithWebIdentity` | `kube-iam-assume setup aws` |
| GCP | Workload Identity Federation | `kube-iam-assume setup gcp` |
| Azure | Federated Identity Credentials | `kube-iam-assume setup azure` |
| HashiCorp Vault | JWT auth method (`auth/jwt`) | `kube-iam-assume setup vault` (v0.2) |

**Vault is a first-class use case.** Self-hosted Kubernetes teams almost always run self-hosted Vault. With kube-iam-assume, Vault can validate service account tokens from external or HCP Vault instances with no network path to your API server — the same pattern cloud providers use. See [ARCHITECTURE.md — Vault Integration](ARCHITECTURE.md#vault-integration) for configuration details.

---

## Roadmap

| Version | What Ships |
|---|---|
| **v0.1** | S3 publishing, key rotation, fleet mode, AWS CLI setup, Helm chart |
| **v0.2** | GCS + Azure Blob, GCP + Azure + Vault CLI setup, Terraform modules, Prometheus metrics |
| **v0.3** | `CloudIdentityBinding` CRD, mutating webhook for automatic credential injection |
| **v1.0** | Built-in HTTPS endpoint, CNCF Landscape submission |

---

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — internal design, token exchange flow, key rotation, fleet mode, Vault integration, security model, configuration reference

---

## Contributing

Areas where help is most needed:

- GCP and Azure publishing backends
- Testing on additional distributions (OpenShift, MicroK8s, Charmed Kubernetes)
- Terraform modules for cloud-side OIDC registration

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

---

## Acknowledgements

Inspired by [amazon-eks-pod-identity-webhook](https://github.com/aws/amazon-eks-pod-identity-webhook), which pioneered IRSA on EKS and proved that service account tokens + OIDC federation is the right model for secretless cloud access. kube-iam-assume brings that same model to every cluster.

---

## License

GNU General Public License v3.0 (GPL-3.0)
