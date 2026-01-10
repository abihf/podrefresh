package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"log/slog"

	loader "github.com/abihf/cache-loader"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	regv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := _main(); err != nil {
		panic(err)
	}
}

var allowedOwnerKinds = map[string]bool{
	"ReplicaSet":  true,
	"DaemonSet":   true,
	"StatefulSet": true,
}

type RegistryAuths map[string]*authn.Basic

var NeedUpdateErr = fmt.Errorf("need update")
var loaderTtl = time.Hour * 24

func _main() error {
	ctx := context.Background()
	clientset, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	globalImageHashCache := loader.New(latestImageHashFetcher(RegistryAuths{}), loaderTtl, loader.WithContextFactory(func() context.Context { return egCtx }))

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		owner := pod.ObjectMeta.OwnerReferences
		if len(owner) == 0 {
			continue
		}
		if !allowedOwnerKinds[owner[0].Kind] {
			continue
		}

		hasAlwaysPull := false
		containerAlwaysPull := make(map[string]bool)
		for _, container := range pod.Spec.Containers {
			if container.ImagePullPolicy == "Always" {
				hasAlwaysPull = true
				containerAlwaysPull[container.Name] = true
				break
			}
		}
		for _, container := range pod.Spec.InitContainers {
			if container.ImagePullPolicy == "Always" {
				hasAlwaysPull = true
				containerAlwaysPull[container.Name] = true
				break
			}
		}
		if !hasAlwaysPull {
			continue
		}

		eg.Go(func() error {
			auth := make(RegistryAuths)
			for _, secretRef := range pod.Spec.ImagePullSecrets {
				secret, err := clientset.CoreV1().Secrets(pod.Namespace).Get(egCtx, secretRef.Name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get image pull secret %s/%s: %w", pod.Namespace, secretRef.Name, err)
				}
				if secret.Type != corev1.SecretTypeDockerConfigJson {
					continue
				}
				dockerConfigJson, ok := secret.Data[corev1.DockerConfigJsonKey]
				if !ok {
					continue
				}
				configs, err := parseDockerConfigJson(dockerConfigJson)
				if err != nil {
					return fmt.Errorf("failed to parse docker config json from secret %s/%s: %w", pod.Namespace, secretRef.Name, err)
				}
				for registry, conf := range configs.Auths {
					auth[registry] = &authn.Basic{
						Username: conf.Username,
						Password: conf.Password,
					}
				}
			}

			feg, fegCtx := errgroup.WithContext(egCtx)
			imageHashCache := globalImageHashCache
			if len(auth) > 0 {
				imageHashCache = loader.New(latestImageHashFetcher(auth), loaderTtl, loader.WithContextFactory(func() context.Context { return fegCtx }))
			}

			for _, status := range pod.Status.ContainerStatuses {
				if !containerAlwaysPull[status.Name] {
					continue
				}
				feg.Go(func() error {
					currentHash := strings.Split(status.ImageID, ":")[1]
					latestHash, err := imageHashCache.Load(status.Image)
					if err != nil {
						slog.Warn("failed to get latest image hash for image", "image", status.Image, "error", err)
						return nil
					}
					if currentHash != latestHash {
						return NeedUpdateErr
					}
					return nil
				})
			}
			err = feg.Wait()
			if err != nil && err != NeedUpdateErr {
				return fmt.Errorf("failed to check images for pod %s/%s: %w", pod.Namespace, pod.Name, err)
			}
			if err == NeedUpdateErr {
				err = clientset.CoreV1().Pods(pod.Namespace).Delete(egCtx, pod.Name, metav1.DeleteOptions{})
				if err != nil {
					return fmt.Errorf("failed to delete pod %s/%s: %w", pod.Namespace, pod.Name, err)
				}
				slog.Info("Deleted pod to force image pull", "namespace", pod.Namespace, "name", pod.Name)
			}
			return nil
		})
	}

	return eg.Wait()
}

func latestImageHashFetcher(auth RegistryAuths) func(ctx context.Context, image string) (string, error) {
	return func(ctx context.Context, image string) (string, error) {
		return getLatestImageHash(ctx, image, auth)
	}
}

func getLatestImageHash(ctx context.Context, image string, auth RegistryAuths) (string, error) {
	// Parse image reference
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}

	// Get remote image descriptor
	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(auth),
		remote.WithPlatform(regv1.Platform{
			Architecture: "amd64",
			OS:           "linux",
		}))
	if err != nil {
		return "", fmt.Errorf("failed to get image descriptor: %w", err)
	}
	return desc.Digest.Hex, nil
}

func (r RegistryAuths) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	registry := resource.RegistryStr()

	// Try exact match
	if cred, ok := r[registry]; ok {
		return cred, nil
	}

	// Try without port
	registryWithoutPort := strings.Split(registry, ":")[0]
	if cred, ok := r[registryWithoutPort]; ok {
		return cred, nil
	}

	// Fallback to anonymous
	return authn.Anonymous, nil
}

type DockerConfig struct {
	Auths map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"auths"`
}

func parseDockerConfigJson(data []byte) (*DockerConfig, error) {
	var config DockerConfig
	err := json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func getK8sClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local kubeconfig
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
	}
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}
