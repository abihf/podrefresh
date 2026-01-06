# PodRefresh

A Kubernetes controller that automatically refreshes pods when their container images are updated in the registry.

## Overview

PodRefresh monitors Kubernetes pods with `imagePullPolicy: Always` and checks if newer versions of their container images are available in the registry. When a newer image is detected (based on digest comparison), it automatically deletes the pod, triggering the controller (ReplicaSet, DaemonSet, or StatefulSet) to recreate it with the latest image.

## Features

- ✅ Automatic image digest comparison with container registries
- ✅ Support for Docker Hub and private registries
- ✅ Authentication via Kubernetes `imagePullSecrets`
- ✅ Only processes pods with `imagePullPolicy: Always`
- ✅ Works with ReplicaSets, DaemonSets, and StatefulSets
- ✅ Minimal resource footprint (built on scratch)

## How It Works

1. Lists all pods across all namespaces
2. Filters pods with `imagePullPolicy: Always` and valid owner references
3. Reads authentication credentials from pod's `imagePullSecrets`
4. Queries the container registry for the latest image digest
5. Compares registry digest with the pod's current image digest
6. Deletes pods when newer images are available, triggering automatic recreation

## Installation

### Using kubectl

```bash
kubectl apply -f https://raw.githubusercontent.com/abihf/podrefresh/main/deploy.yaml
```

### From Source

```bash
git clone https://github.com/abihf/podrefresh.git
cd podrefresh
go build -o podrefresh .
```

## Configuration

The controller runs as a Kubernetes CronJob at midnight daily. It requires the following permissions:

```yaml
- pods: get, list, delete
- secrets: get (for imagePullSecrets)
```

See [deploy.yaml](deploy.yaml) for the complete manifest including ServiceAccount, RBAC, and CronJob configuration.

## Usage with Private Registries

Create an `imagePullSecret` for your private registry:

```bash
kubectl create secret docker-registry my-registry-secret \
  --docker-server=registry.example.com \
  --docker-username=your-username \
  --docker-password=your-password
```

Reference it in your pod specification:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
spec:
  imagePullSecrets:
  - name: my-registry-secret
  containers:
  - name: app
    image: registry.example.com/my-app:latest
    imagePullPolicy: Always
```

## Supported Registries

- Docker Hub (`docker.io`)
- GitHub Container Registry (`ghcr.io`)
- Google Container Registry (`gcr.io`)
- AWS Elastic Container Registry (ECR)
- Azure Container Registry (ACR)
- Any Docker Registry V2-compliant registry

## Development

### Build

```bash
go build -o podrefresh .
```

### Run Locally

```bash
# Requires valid kubeconfig
./podrefresh
```

### Build Docker Image

```bash
docker build -t podrefresh:dev .
```

## Dependencies

- [kubernetes/client-go](https://github.com/kubernetes/client-go) - Kubernetes client library
- [google/go-containerregistry](https://github.com/google/go-containerregistry) - Container registry interactions

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Security Considerations

- Ensure proper RBAC configuration to limit access
- Use read-only credentials in `imagePullSecrets`
- Monitor controller logs for unauthorized access attempts
- Consider rate limiting for large clusters with many pods

## Troubleshooting

### Pod not being refreshed

- Verify `imagePullPolicy: Always` is set on the container
- Check pod has valid owner reference (ReplicaSet/DaemonSet/StatefulSet)
- Ensure `imagePullSecrets` are correctly configured
- Check controller logs for authentication errors

### Authentication failures

- Verify secret type is `kubernetes.io/dockerconfigjson`
- Ensure credentials have pull permissions
- Check registry URL matches exactly (including port if non-standard)
